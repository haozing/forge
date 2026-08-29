package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// Coordinator consumes phase 0/2 domain facts and translates them into
// projection runs and River jobs. Every ProcessFact call performs only short
// transactions: ensure runs, enqueue jobs, never canonicalize or call
// providers here (doc §9.1/§9.2).
type Coordinator struct {
	Store *store.Store
	// Queue inserts River jobs inside the coordination transaction.
	Queue eventing.QueueClient
}

// ProcessFact handles one domain fact event.
func (c Coordinator) ProcessFact(ctx context.Context, eventType string, payload json.RawMessage) error {
	if c.Store == nil || c.Store.Pool == nil {
		return fmt.Errorf("database store is not initialized")
	}
	switch eventType {
	case eventing.EventAssetPublished:
		var fact eventing.AssetPublishedPayload
		if err := json.Unmarshal(payload, &fact); err != nil || fact.AssetID == "" || fact.VersionID == "" {
			return fmt.Errorf("asset.published payload is invalid")
		}
		return c.handleAssetPublished(ctx, fact)
	case eventing.EventAssetArchived:
		var fact eventing.AssetArchivedPayload
		if err := json.Unmarshal(payload, &fact); err != nil || fact.AssetID == "" {
			return fmt.Errorf("asset.archived payload is invalid")
		}
		return c.handleAssetArchived(ctx, fact)
	case eventing.EventAssetVisibilityChanged, eventing.EventAssetRestored,
		eventing.EventAssetVersionCreated, eventing.EventTagCreated,
		eventing.EventTagArchived, eventing.EventTagRestored:
		// No projection work: primary-data final authorization and structured
		// queries pick the change up immediately.
		return nil
	case eventing.EventTagUpdated:
		var fact eventing.TagUpdatedPayload
		if err := json.Unmarshal(payload, &fact); err != nil || fact.TagID == "" {
			return fmt.Errorf("tag.updated payload is invalid")
		}
		return c.handleTagUpdated(ctx, fact)
	case eventing.EventResourceModelPolicyPublished:
		var fact eventing.ResourceModelPolicyPublishedPayload
		if err := json.Unmarshal(payload, &fact); err != nil || fact.ResourceModelID == "" || fact.VersionID == "" {
			return fmt.Errorf("resource_model.policy_published payload is invalid")
		}
		return c.handlePolicyPublished(ctx, fact)
	default:
		// Unknown facts are consumed successfully: the registry only routes
		// the event types declared for this consumer.
		return nil
	}
}

// handleAssetPublished ensures runs for the current published version under
// the active and warming profiles.
func (c Coordinator) handleAssetPublished(ctx context.Context, fact eventing.AssetPublishedPayload) error {
	organizationID, workspaceID, resourceModelID, resourceModelVersionID, current, err := c.loadVersion(ctx, fact.AssetID, fact.VersionID)
	if err != nil {
		return err
	}
	if !current {
		// A later publish/archive already moved the pointer; nothing to do.
		return nil
	}
	return c.ensureRunsForVersion(ctx, ensureRunKey{
		OrganizationID: organizationID, WorkspaceID: workspaceID,
		AssetID: fact.AssetID, AssetVersionID: fact.VersionID,
		ResourceModelID: resourceModelID, ResourceModelVersionID: resourceModelVersionID,
	})
}

// handleAssetArchived stales the previous published runs and drops heads.
func (c Coordinator) handleAssetArchived(ctx context.Context, fact eventing.AssetArchivedPayload) error {
	if fact.PreviousVersionID == "" {
		return nil
	}
	organizationID, err := c.organizationOfAsset(ctx, fact.AssetID)
	if err != nil {
		return err
	}
	if organizationID == "" {
		return nil
	}
	repo := RunRepository{Store: c.Store}
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin archive coordination: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := repo.MarkAssetRunsStaleTx(ctx, tx, organizationID, fact.AssetID, fact.PreviousVersionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// handleTagUpdated rebuilds the canonical text when the display name (part of
// the tag segment) changed; other fields never enter the canonical body.
func (c Coordinator) handleTagUpdated(ctx context.Context, fact eventing.TagUpdatedPayload) error {
	displayNameChanged := false
	for _, field := range fact.ChangedFields {
		if strings.EqualFold(strings.TrimSpace(field), "display_name") {
			displayNameChanged = true
			break
		}
	}
	if !displayNameChanged {
		return nil
	}
	organizationID, err := c.organizationOfTag(ctx, fact.TagID)
	if err != nil {
		return err
	}
	if organizationID == "" {
		return nil
	}
	versions, err := c.taggedPublishedVersions(ctx, organizationID, fact.TagID)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if err := c.publishFactEnsure(ctx, organizationID, version); err != nil {
			return err
		}
	}
	return nil
}

// handlePolicyPublished reconciles eligibility and backfills only the
// published assets already bound to the new resource model version
// (doc §7.1).
func (c Coordinator) handlePolicyPublished(ctx context.Context, fact eventing.ResourceModelPolicyPublishedPayload) error {
	organizationID, err := c.organizationOfResourceModel(ctx, fact.ResourceModelID)
	if err != nil {
		return err
	}
	if organizationID == "" {
		return nil
	}
	versions, err := c.boundPublishedVersions(ctx, organizationID, fact.VersionID)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if err := c.publishFactEnsure(ctx, organizationID, version); err != nil {
			return err
		}
	}
	return nil
}

type ensureRunKey struct {
	OrganizationID         string
	WorkspaceID            string
	AssetID                string
	AssetVersionID         string
	ResourceModelID        string
	ResourceModelVersionID string
}

// ensureRunsForVersion ensures one run per serving profile inside one short
// transaction and enqueues the build jobs.
func (c Coordinator) ensureRunsForVersion(ctx context.Context, key ensureRunKey) error {
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin projection coordination: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := c.ensureRunsTx(ctx, tx, key); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit projection coordination: %w", err)
	}
	return nil
}

// ensureRunsTx inserts the queued runs and their River jobs inside tx.
func (c Coordinator) ensureRunsTx(ctx context.Context, tx pgx.Tx, key ensureRunKey) (int, error) {
	profiles, err := servingProfilesTx(ctx, tx, key.OrganizationID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, profile := range profiles {
		eligible, err := BuildEligibilityTx(ctx, tx, key.OrganizationID, key.AssetVersionID)
		if err != nil {
			return created, err
		}
		if !eligible {
			continue
		}
		runID, err := EnsureQueuedRunTx(ctx, tx, key.OrganizationID, key.WorkspaceID,
			key.AssetID, key.AssetVersionID, key.ResourceModelID, key.ResourceModelVersionID,
			profile.ID, profile.SemanticEnabled)
		if err != nil {
			return created, err
		}
		if c.Queue != nil {
			if _, err := c.Queue.InsertTx(ctx, tx, BuildProjectionRunArgs{RunID: runID}, nil); err != nil {
				return created, fmt.Errorf("enqueue retrieval build job: %w", err)
			}
		}
		created++
	}
	return created, nil
}

// publishFactEnsure resolves version facts for secondary triggers (tag
// rename, policy publish) and ensures runs.
func (c Coordinator) publishFactEnsure(ctx context.Context, organizationID string, versionID string) error {
	var workspaceID, assetID, resourceModelID, resourceModelVersionID string
	err := c.Store.Pool.QueryRow(ctx, `
		SELECT v.workspace_id::text, v.asset_id::text, v.resource_model_id::text,
		       v.resource_model_version_id::text
		FROM asset.asset_versions v
		WHERE v.organization_id = $1::uuid AND v.id = $2::uuid
	`, organizationID, versionID).Scan(&workspaceID, &assetID, &resourceModelID, &resourceModelVersionID)
	if err != nil {
		// The version may be gone; the trigger is then moot.
		return nil
	}
	return c.ensureRunsForVersion(ctx, ensureRunKey{
		OrganizationID: organizationID, WorkspaceID: workspaceID,
		AssetID: assetID, AssetVersionID: versionID,
		ResourceModelID: resourceModelID, ResourceModelVersionID: resourceModelVersionID,
	})
}

type profileRef struct {
	ID              string
	SemanticEnabled bool
}

// servingProfilesTx lists the active and warming profiles (doc §9.2).
func servingProfilesTx(ctx context.Context, tx pgx.Tx, organizationID string) ([]profileRef, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, semantic_enabled FROM retrieval.projection_profiles
		WHERE organization_id = $1::uuid AND status IN ('active','warming')
		ORDER BY generation DESC
	`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list serving retrieval profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]profileRef, 0, 2)
	for rows.Next() {
		var profile profileRef
		if err := rows.Scan(&profile.ID, &profile.SemanticEnabled); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// loadVersion resolves the immutable version facts and confirms the version
// is the current published pointer.
func (c Coordinator) loadVersion(ctx context.Context, assetID, versionID string) (organizationID, workspaceID, resourceModelID, resourceModelVersionID string, current bool, err error) {
	err = c.Store.Pool.QueryRow(ctx, `
		SELECT a.organization_id::text, a.workspace_id::text, a.resource_model_id::text,
		       v.resource_model_version_id::text,
		       (a.publication_status = 'published' AND a.current_published_version_id = v.id)
		FROM asset.assets a
		JOIN asset.asset_versions v
			ON v.organization_id = a.organization_id AND v.id = $2::uuid
		WHERE a.id = $1::uuid
	`, assetID, versionID).Scan(&organizationID, &workspaceID, &resourceModelID, &resourceModelVersionID, &current)
	if err != nil {
		// Unknown asset/version: the fact concerns a deleted asset; consume.
		return "", "", "", "", false, nil
	}
	return organizationID, workspaceID, resourceModelID, resourceModelVersionID, current, nil
}

func (c Coordinator) organizationOfAsset(ctx context.Context, assetID string) (string, error) {
	var organizationID string
	err := c.Store.Pool.QueryRow(ctx,
		`SELECT organization_id::text FROM asset.assets WHERE id = $1::uuid`, assetID).Scan(&organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Deleted assets have no runs left to stale.
		return "", nil
	}
	return organizationID, err
}

func (c Coordinator) organizationOfTag(ctx context.Context, tagID string) (string, error) {
	var organizationID string
	err := c.Store.Pool.QueryRow(ctx,
		`SELECT organization_id::text FROM asset.tags WHERE id = $1::uuid`, tagID).Scan(&organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return organizationID, err
}

func (c Coordinator) organizationOfResourceModel(ctx context.Context, resourceModelID string) (string, error) {
	var organizationID string
	err := c.Store.Pool.QueryRow(ctx,
		`SELECT organization_id::text FROM model.resource_models WHERE id = $1::uuid`, resourceModelID).Scan(&organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return organizationID, err
}

// taggedPublishedVersions finds the current published versions related to
// one tag (display name changed).
func (c Coordinator) taggedPublishedVersions(ctx context.Context, organizationID, tagID string) ([]string, error) {
	rows, err := c.Store.Pool.Query(ctx, `
		SELECT v.id::text
		FROM asset.asset_version_tags avt
		JOIN asset.asset_versions v ON v.organization_id = avt.organization_id AND v.id = avt.asset_version_id
		JOIN asset.assets a ON a.organization_id = v.organization_id AND a.id = v.asset_id
		WHERE avt.organization_id = $1::uuid AND avt.tag_id = $2::uuid
		  AND a.publication_status = 'published'
		  AND a.current_published_version_id = v.id
	`, organizationID, tagID)
	if err != nil {
		return nil, fmt.Errorf("list tagged published versions: %w", err)
	}
	defer rows.Close()
	return collectStrings(rows)
}

// boundPublishedVersions finds the current published versions bound to one
// resource model version (policy published).
func (c Coordinator) boundPublishedVersions(ctx context.Context, organizationID, modelVersionID string) ([]string, error) {
	rows, err := c.Store.Pool.Query(ctx, `
		SELECT v.id::text
		FROM asset.assets a
		JOIN asset.asset_versions v
			ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		WHERE a.organization_id = $1::uuid
		  AND a.publication_status = 'published'
		  AND v.resource_model_version_id = $2::uuid
	`, organizationID, modelVersionID)
	if err != nil {
		return nil, fmt.Errorf("list bound published versions: %w", err)
	}
	defer rows.Close()
	return collectStrings(rows)
}

func collectStrings(rows pgx.Rows) ([]string, error) {
	values := make([]string, 0, 8)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
