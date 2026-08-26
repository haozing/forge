package asset

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("asset or asset version not found")
var ErrConflict = errors.New("asset state conflict")
var ErrInvalidInput = errors.New("invalid asset input")
var ErrForbidden = errors.New("asset access denied")

type Service struct {
	Store  *store.Store
	Events eventing.EventStore
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

func (s Service) Publish(ctx context.Context, principal auth.Principal, allowedModelIDs []string, assetID, versionID string) (PublishResult, error) {
	if !validID(assetID) || !validID(versionID) || len(allowedModelIDs) == 0 {
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
	var organizationID, workflowStatus string
	var previousPublishedID *string
	err = tx.QueryRow(ctx, `
		SELECT a.organization_id::text, a.current_published_version_id::text, v.workflow_status
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.id = $2::uuid AND v.asset_id = a.id
		WHERE a.id = $1::uuid AND a.organization_id = $3::uuid
		  AND a.resource_model_id::text = ANY($4::text[])
		  AND a.current_working_version_id = v.id
		FOR UPDATE OF a, v
	`, assetID, versionID, principal.OrganizationID, allowedModelIDs).Scan(&organizationID, &previousPublishedID, &workflowStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublishResult{}, ErrNotFound
	}
	if err != nil {
		return PublishResult{}, fmt.Errorf("load asset for publish: %w", err)
	}
	if workflowStatus != "draft" {
		return PublishResult{}, fmt.Errorf("%w: version is still processing", ErrConflict)
	}
	var hasUnsafeAttachments bool
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE version_lineage(id) AS (
			SELECT $2::uuid
			UNION ALL
			SELECT av.parent_version_id
			FROM asset.asset_versions av
			JOIN version_lineage child ON av.id = child.id
			WHERE av.parent_version_id IS NOT NULL
		)
		SELECT EXISTS (
			SELECT 1
			FROM asset.attachments at
			WHERE at.organization_id = $1::uuid
			  AND at.deleted_at IS NULL
			  AND at.scan_status <> 'clean'
			  AND (
				at.asset_version_id IN (SELECT id FROM version_lineage)
				OR EXISTS (
					SELECT 1 FROM asset.attachment_links al
					WHERE al.attachment_id = at.id
					  AND al.asset_version_id IN (SELECT id FROM version_lineage)
				)
			  )
		)
	`, principal.OrganizationID, versionID).Scan(&hasUnsafeAttachments); err != nil {
		return PublishResult{}, fmt.Errorf("check attachment scan status: %w", err)
	}
	if hasUnsafeAttachments {
		return PublishResult{}, fmt.Errorf("%w: all attachments must be clean before publish", ErrConflict)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_published_version_id = $2::uuid, publication_status = 'published', updated_at = now() WHERE id = $1::uuid`, assetID, versionID); err != nil {
		return PublishResult{}, fmt.Errorf("publish asset: %w", err)
	}
	if previousPublishedID != nil && *previousPublishedID != versionID {
		if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, organizationID, *previousPublishedID, retrieval.ProjectionDelete); err != nil {
			return PublishResult{}, fmt.Errorf("enqueue previous projection deletion: %w", err)
		}
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, organizationID, versionID, retrieval.ProjectionRebuild); err != nil {
		return PublishResult{}, fmt.Errorf("enqueue published projection rebuild: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata) VALUES ($1::uuid, $2::uuid, $2::uuid, 'asset.publish', 'asset', $3::uuid, 'allowed', jsonb_build_object('principal_type', $4::text, 'review_required', false))`, principal.OrganizationID, principal.UserID, assetID, principal.UserType); err != nil {
		return PublishResult{}, fmt.Errorf("record publish audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PublishResult{}, fmt.Errorf("commit publish transaction: %w", err)
	}
	return PublishResult{AssetID: assetID, PublishedVersionID: versionID, PreviousPublishedID: previousPublishedID, PublicationStatus: "published"}, nil
}

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
	var organizationID, previousPublishedID string
	err = tx.QueryRow(ctx, `
		SELECT organization_id::text, current_published_version_id::text
		FROM asset.assets
		WHERE id = $1::uuid AND organization_id = $2::uuid
		  AND resource_model_id::text = ANY($3::text[]) AND current_published_version_id IS NOT NULL
		FOR UPDATE
	`, assetID, principal.OrganizationID, allowedModelIDs).Scan(&organizationID, &previousPublishedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ArchiveResult{}, ErrNotFound
	}
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("load asset for archive: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_published_version_id = NULL, publication_status = 'archived', updated_at = now() WHERE id = $1::uuid`, assetID); err != nil {
		return ArchiveResult{}, fmt.Errorf("archive asset: %w", err)
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, organizationID, previousPublishedID, retrieval.ProjectionDelete); err != nil {
		return ArchiveResult{}, fmt.Errorf("enqueue archived projection deletion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ArchiveResult{}, fmt.Errorf("commit archive transaction: %w", err)
	}
	return ArchiveResult{AssetID: assetID, PreviousPublishedID: previousPublishedID, PublicationStatus: "archived"}, nil
}

func validID(value string) bool { return uuidPattern.MatchString(value) }

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
