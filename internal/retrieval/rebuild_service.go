package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// Rebuild scope types and reasons (retrieval.projection_rebuilds).
const (
	ScopeOrganization  = "organization"
	ScopeWorkspace     = "workspace"
	ScopeResourceModel = "resource_model"
	ScopeAsset         = "asset"

	ReasonProfileWarming = "profile_warming"
	ReasonManual         = "manual"
	ReasonPolicyChanged  = "policy_changed"
	ReasonRepair         = "repair"
)

// RebuildService starts rebuild batches and reports warming coverage.
type RebuildService struct {
	Store *store.Store
	Queue QueueInserter
}

// Rebuild is one retrieval.projection_rebuilds row.
type Rebuild struct {
	ID             string
	OrganizationID string
	WorkspaceID    string
	ProfileID      string
	ScopeType      string
	ScopeID        string
	Reason         string
	Status         string
	TotalCount     int
	QueuedCount    int
	ReadyCount     int
	DegradedCount  int
	FailedCount    int
	IdempotencyKey string
	RequestedBy    string
}

// StartRebuild records the batch and enqueues the backfill worker. The
// idempotency key collapses duplicate requests onto the same row.
func (s RebuildService) StartRebuild(ctx context.Context, organizationID string, scopeType, workspaceID, resourceModelID, assetID, reason, requestedBy, idempotencyKey string) (Rebuild, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Rebuild{}, errors.New("database store is not initialized")
	}
	scopeID := ""
	switch scopeType {
	case ScopeOrganization:
		if workspaceID != "" || assetID != "" {
			return Rebuild{}, fmt.Errorf("%w: organization scope takes no asset id", ErrInvalidScope)
		}
	case ScopeWorkspace:
		if workspaceID == "" {
			return Rebuild{}, fmt.Errorf("%w: workspace scope requires a workspace id", ErrInvalidScope)
		}
		scopeID = workspaceID
	case ScopeResourceModel:
		if resourceModelID == "" {
			return Rebuild{}, fmt.Errorf("%w: resource model scope requires a model id", ErrInvalidScope)
		}
		scopeID = resourceModelID
	case ScopeAsset:
		if assetID == "" {
			return Rebuild{}, fmt.Errorf("%w: asset scope requires an asset id", ErrInvalidScope)
		}
		scopeID = assetID
	default:
		return Rebuild{}, fmt.Errorf("%w: unknown scope type %q", ErrInvalidScope, scopeType)
	}
	reason = normalizeRebuildReason(reason)

	profileID, err := s.resolveProfileID(ctx, organizationID)
	if err != nil {
		return Rebuild{}, err
	}
	requestHash := rebuildRequestHash(organizationID, scopeType, scopeID, reason, profileID)

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Rebuild{}, fmt.Errorf("begin rebuild: %w", err)
	}
	defer tx.Rollback(ctx)
	rebuild, err := insertRebuildTx(ctx, tx, rebuildRow{
		OrganizationID: organizationID, WorkspaceID: workspaceID, ProfileID: profileID,
		ScopeType: scopeType, ScopeID: scopeID, Reason: reason,
		IdempotencyKey: idempotencyKey, RequestedBy: requestedBy, RequestHash: requestHash,
	})
	if err != nil {
		return Rebuild{}, err
	}
	if rebuild.Status == statusQueued && s.Queue != nil {
		if _, err := s.Queue.Insert(ctx, BackfillProfileArgs{RebuildID: rebuild.ID, Page: 0}, nil); err != nil {
			return Rebuild{}, fmt.Errorf("enqueue retrieval backfill: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Rebuild{}, fmt.Errorf("commit rebuild: %w", err)
	}
	return rebuild, nil
}

type rebuildRow struct {
	OrganizationID string
	WorkspaceID    string
	ProfileID      string
	ScopeType      string
	ScopeID        string
	Reason         string
	IdempotencyKey string
	RequestedBy    string
	RequestHash    string
}

const statusQueued = "queued"

func insertRebuildTx(ctx context.Context, tx pgx.Tx, row rebuildRow) (Rebuild, error) {
	var rebuild Rebuild
	err := tx.QueryRow(ctx, `
		INSERT INTO retrieval.projection_rebuilds
			(organization_id, workspace_id, projection_profile_id, scope_type, scope_id,
			 reason, status, idempotency_key, request_hash, requested_by)
		VALUES ($1::uuid, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, $4, $5, $6, 'queued',
		        NULLIF($7,''), $8, NULLIF($9,'')::uuid)
		ON CONFLICT (organization_id, idempotency_key) DO UPDATE SET updated_at = now()
		RETURNING id::text, organization_id::text, COALESCE(workspace_id::text,''),
		          COALESCE(projection_profile_id::text,''), scope_type, scope_id, reason,
		          status, total_count, queued_count, ready_count, degraded_count, failed_count,
		          COALESCE(idempotency_key,''), COALESCE(requested_by::text,'')
	`, row.OrganizationID, row.WorkspaceID, row.ProfileID, row.ScopeType, row.ScopeID,
		row.Reason, row.IdempotencyKey, row.RequestHash, row.RequestedBy).Scan(
		&rebuild.ID, &rebuild.OrganizationID, &rebuild.WorkspaceID, &rebuild.ProfileID,
		&rebuild.ScopeType, &rebuild.ScopeID, &rebuild.Reason, &rebuild.Status,
		&rebuild.TotalCount, &rebuild.QueuedCount, &rebuild.ReadyCount,
		&rebuild.DegradedCount, &rebuild.FailedCount, &rebuild.IdempotencyKey,
		&rebuild.RequestedBy)
	if err != nil {
		return Rebuild{}, fmt.Errorf("insert retrieval rebuild: %w", err)
	}
	return rebuild, nil
}

func normalizeRebuildReason(reason string) string {
	switch reason {
	case ReasonProfileWarming, ReasonPolicyChanged, ReasonRepair:
		return reason
	default:
		return ReasonManual
	}
}

// resolveProfileID picks the profile a rebuild backfills. The active profile
// serves every reason; when only a warming profile exists, any rebuild —
// manual included — backfills that profile: warming an organization that has
// never activated one is exactly the bootstrap semantic.
func (s RebuildService) resolveProfileID(ctx context.Context, organizationID string) (string, error) {
	repo := ProfileRepository{Store: s.Store}
	profile, err := repo.GetActiveProfile(ctx, organizationID)
	if errors.Is(err, ErrNoActiveProfile) {
		warming, warmErr := repo.GetWarmingProfile(ctx, organizationID)
		if errors.Is(warmErr, pgx.ErrNoRows) {
			return "", ErrNoActiveProfile
		}
		if warmErr != nil {
			return "", warmErr
		}
		return warming.ID, nil
	}
	return profile.ID, err
}

func rebuildRequestHash(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(digest[:])
}

// WarmingCoverage reports the run status distribution of one profile; the
// activation gate in ProfileService uses the eligible/covered numbers.
func (s RebuildService) WarmingCoverage(ctx context.Context, organizationID, profileID string) (map[string]int, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return nil, errors.New("database store is not initialized")
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT status, count(*)
		FROM retrieval.projection_runs
		WHERE organization_id = $1::uuid AND projection_profile_id = $2::uuid
		GROUP BY status
	`, organizationID, profileID)
	if err != nil {
		return nil, fmt.Errorf("count retrieval runs by status: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// ProcessBackfillPage ensures runs for one bounded page of eligible versions
// of the rebuild scope, updates the batch counters and either completes the
// batch or schedules the next page.
func ProcessBackfillPage(ctx context.Context, st *store.Store, queue QueueInserter, args BackfillProfileArgs) error {
	var (
		organizationID, profileID, scopeType, scopeID, workspaceID, reason string
	)
	err := st.Pool.QueryRow(ctx, `
		SELECT organization_id::text, COALESCE(projection_profile_id::text,''),
		       scope_type, scope_id, COALESCE(workspace_id::text,''), reason
		FROM retrieval.projection_rebuilds
		WHERE id = $1::uuid AND status IN ('queued','running')
	`, args.RebuildID).Scan(&organizationID, &profileID, &scopeType, &scopeID, &workspaceID, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load retrieval rebuild: %w", err)
	}
	if profileID == "" {
		return fmt.Errorf("retrieval rebuild %s has no profile", args.RebuildID)
	}

	offset := args.Page * BackfillPageSize
	rows, err := st.Pool.Query(ctx, `
		SELECT a.organization_id::text, a.workspace_id::text, a.id::text,
		       v.id::text, v.resource_model_id::text, v.resource_model_version_id::text,
		       COALESCE((SELECT semantic_enabled FROM retrieval.projection_profiles
		                 WHERE id = $2::uuid), false)
		FROM asset.assets a
		JOIN asset.asset_versions v
			ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv
			ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
		WHERE a.organization_id = $1::uuid
		  AND a.publication_status = 'published'
		  AND a.deleted_at IS NULL
		  AND (
		        COALESCE(mv.policy #>> '{retrieval,fulltext,enabled}','')::boolean
		        OR COALESCE(mv.policy #>> '{retrieval,semantic,enabled}','')::boolean
		      )
		  AND EXISTS (
		        SELECT 1 FROM jsonb_object_keys(COALESCE(mv.policy->'channels','{}'::jsonb)) AS channel
		        WHERE COALESCE(mv.policy #> ('{channels,'||channel||',enabled}')::text[], '')::boolean
		      )
		  AND CASE $3
		        WHEN 'workspace' THEN a.workspace_id::text = $4
		        WHEN 'resource_model' THEN v.resource_model_id::text = $4
		        WHEN 'asset' THEN a.id::text = $4
		        ELSE TRUE
		      END
		ORDER BY a.id, v.id
		OFFSET $5 LIMIT $6
	`, organizationID, profileID, scopeType, scopeID, offset, BackfillPageSize+1)
	if err != nil {
		return fmt.Errorf("page retrieval rebuild versions: %w", err)
	}
	type versionKey struct {
		OrganizationID, WorkspaceID, AssetID, VersionID, ModelID, ModelVersionID string
		SemanticEnabled                                                          bool
	}
	var versions []versionKey
	for rows.Next() {
		var key versionKey
		if err := rows.Scan(&key.OrganizationID, &key.WorkspaceID, &key.AssetID,
			&key.VersionID, &key.ModelID, &key.ModelVersionID, &key.SemanticEnabled); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	more := len(versions) > BackfillPageSize
	if more {
		versions = versions[:BackfillPageSize]
	}

	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin backfill page: %w", err)
	}
	defer tx.Rollback(ctx)
	for _, key := range versions {
		runID, err := EnsureQueuedRunTx(ctx, tx, key.OrganizationID, key.WorkspaceID,
			key.AssetID, key.VersionID, key.ModelID, key.ModelVersionID, profileID, key.SemanticEnabled)
		if err != nil {
			return err
		}
		if queue != nil {
			if _, err := queue.Insert(ctx, BuildProjectionRunArgs{RunID: runID}, nil); err != nil {
				return fmt.Errorf("enqueue retrieval build job: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE retrieval.projection_rebuilds
		SET status = CASE WHEN $2 THEN 'running' ELSE status END,
		    total_count = GREATEST(total_count, $3),
		    queued_count = $4,
		    completed_at = CASE WHEN $2 THEN NULL ELSE COALESCE(completed_at, now()) END
		WHERE id = $1::uuid
	`, args.RebuildID, more, offset+len(versions), len(versions)); err != nil {
		return fmt.Errorf("update retrieval rebuild counters: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit backfill page: %w", err)
	}
	if more && queue != nil {
		if _, err := queue.Insert(ctx, BackfillProfileArgs{RebuildID: args.RebuildID, Page: args.Page + 1}, nil); err != nil {
			return fmt.Errorf("enqueue retrieval backfill page: %w", err)
		}
	}
	return nil
}
