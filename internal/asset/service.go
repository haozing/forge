package asset

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

func pgxNoRows() error { return pgx.ErrNoRows }

// Agent-facing publish/archive. The lifecycle state machine, pointer
// invariants, audit and fact events are shared with the member service;
// retrieval is not contacted here — the worker consumes asset.published /
// asset.archived facts (phase 0 decoupling).

type Service struct {
	Store  *store.Store
	Events *eventing.EventStore
	Policy authz.WorkspacePolicy
}

type PublishResult struct {
	AssetID             string  `json:"asset_id"`
	PublishedVersionID  string  `json:"published_version_id"`
	PreviousPublishedID *string `json:"previous_published_version_id,omitempty"`
	PublicationStatus   string  `json:"publication_status"`
}

type ArchiveResult struct {
	AssetID             string `json:"asset_id"`
	PreviousPublishedID string `json:"previous_published_version_id"`
	PublicationStatus   string `json:"publication_status"`
}

// Publish points the published marker at the asset's current working version.
// The agent principal must hold asset.publish for the asset's model through
// its AgentAccessPolicy; version selection by the caller is not part of the
// v2 contract.
func (s Service) Publish(ctx context.Context, principal auth.Principal, allowedModelIDs []string, assetID, versionID string) (PublishResult, error) {
	if !validID(assetID) || len(allowedModelIDs) == 0 {
		return PublishResult{}, ErrNotFound
	}
	if s.Store == nil || s.Store.Pool == nil {
		return PublishResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return PublishResult{}, fmt.Errorf("begin publish transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	var workspaceID, modelID string
	err = tx.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text
		FROM asset.assets
		WHERE id = $1::uuid AND organization_id = $2::uuid
		  AND resource_model_id::text = ANY($3::text[])
		FOR UPDATE
	`, assetID, principal.OrganizationID, allowedModelIDs).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgxNoRows()) {
		return PublishResult{}, ErrNotFound
	}
	if err != nil {
		return PublishResult{}, fmt.Errorf("load asset for publish: %w", err)
	}
	if s.Policy != nil {
		if _, err := s.Policy.Require(ctx, principal, workspaceID, modelID, authz.ActionAssetPublish); err != nil {
			return PublishResult{}, ErrForbidden
		}
	}
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return PublishResult{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return PublishResult{}, ErrAssetArchived
	}
	previous := row.CurrentPublishedVersionID
	row, err = SetPublishedPointerTx(ctx, tx, row, row.CurrentWorkingVersionID)
	if err != nil {
		return PublishResult{}, err
	}
	if err := AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetPublished, eventing.PayloadVersionV1, eventing.AssetPublishedPayload{
		AssetID:           row.ID,
		VersionID:         row.CurrentWorkingVersionID,
		PreviousVersionID: derefOrEmpty(previous),
		WorkspaceID:       row.WorkspaceID,
	}); err != nil {
		return PublishResult{}, err
	}
	RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.publish", row.ID, map[string]any{
		"workspace_id":   row.WorkspaceID,
		"principal_type": principal.UserType,
	})
	if err := tx.Commit(ctx); err != nil {
		return PublishResult{}, fmt.Errorf("commit publish transaction: %w", err)
	}
	return PublishResult{AssetID: assetID, PublishedVersionID: row.CurrentWorkingVersionID, PreviousPublishedID: previous, PublicationStatus: PublicationPublished}, nil
}

// Archive clears the published pointer for an agent-controlled asset.
func (s Service) Archive(ctx context.Context, principal auth.Principal, allowedModelIDs []string, assetID string) (ArchiveResult, error) {
	if !validID(assetID) || len(allowedModelIDs) == 0 {
		return ArchiveResult{}, ErrNotFound
	}
	if s.Store == nil || s.Store.Pool == nil {
		return ArchiveResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("begin archive transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	var workspaceID, modelID string
	err = tx.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text
		FROM asset.assets
		WHERE id = $1::uuid AND organization_id = $2::uuid
		  AND resource_model_id::text = ANY($3::text[])
		FOR UPDATE
	`, assetID, principal.OrganizationID, allowedModelIDs).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgxNoRows()) {
		return ArchiveResult{}, ErrNotFound
	}
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("load asset for archive: %w", err)
	}
	if s.Policy != nil {
		if _, err := s.Policy.Require(ctx, principal, workspaceID, modelID, authz.ActionAssetArchive); err != nil {
			return ArchiveResult{}, ErrForbidden
		}
	}
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return ArchiveResult{}, err
	}
	previous := row.CurrentPublishedVersionID
	if _, err := CancelPendingRequestsTx(ctx, tx, row.OrganizationID, row.ID, principal.UserID, "asset_archived"); err != nil {
		return ArchiveResult{}, err
	}
	row, err = ClearPublishedPointerTx(ctx, tx, row)
	if err != nil {
		return ArchiveResult{}, err
	}
	if err := AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetArchived, eventing.PayloadVersionV1, eventing.AssetArchivedPayload{
		AssetID:           row.ID,
		PreviousVersionID: derefOrEmpty(previous),
		WorkspaceID:       row.WorkspaceID,
	}); err != nil {
		return ArchiveResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ArchiveResult{}, fmt.Errorf("commit archive transaction: %w", err)
	}
	result := ArchiveResult{AssetID: assetID, PublicationStatus: PublicationArchived}
	if previous != nil {
		result.PreviousPublishedID = *previous
	}
	return result, nil
}

func validID(value string) bool { return uuidPattern.MatchString(value) }

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
