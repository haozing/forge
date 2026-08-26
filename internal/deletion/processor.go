package deletion

import (
	"context"
	"errors"
	"fmt"

	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

type Processor struct {
	Store *store.Store
}

func (p Processor) ProcessNext(ctx context.Context) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin deletion job: %w", err)
	}
	defer tx.Rollback(ctx)

	var jobID, organizationID, workspaceID, resourceType, resourceID, requestedBy string
	err = tx.QueryRow(ctx, `
		SELECT id::text, organization_id::text, workspace_id::text, resource_type, resource_id::text, requested_by::text
		FROM content.deletion_jobs
		WHERE status = 'queued'
		ORDER BY created_at, id
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`).Scan(&jobID, &organizationID, &workspaceID, &resourceType, &resourceID, &requestedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoPendingJob
	}
	if err != nil {
		return fmt.Errorf("claim deletion job: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE content.deletion_jobs SET status = 'running', started_at = now() WHERE id = $1::uuid`, jobID); err != nil {
		return fmt.Errorf("start deletion job: %w", err)
	}

	switch resourceType {
	case "asset":
		if err := processAssetDeletion(ctx, tx, organizationID, workspaceID, resourceID); err != nil {
			return failJob(ctx, tx, jobID, "asset_delete_failed", err)
		}
	case "workspace":
		if err := processWorkspaceDeletion(ctx, tx, organizationID, workspaceID); err != nil {
			return failJob(ctx, tx, jobID, "workspace_delete_failed", err)
		}
	default:
		return failJob(ctx, tx, jobID, "invalid_resource_type", errors.New("unsupported deletion resource type"))
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, $3, $4, $5::uuid, 'allowed',
		        jsonb_build_object('workspace_id', $6::text, 'deletion_job_id', $7::text))
	`, organizationID, requestedBy, resourceType+".delete", resourceType, resourceID, workspaceID, jobID); err != nil {
		return fmt.Errorf("record deletion audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.notifications
			(organization_id, workspace_id, recipient_user_id, type, title, body, object_type, object_id, metadata)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'system', $4, '', 'deletion_job', $5::uuid,
		        jsonb_build_object('resource_type', $6::text, 'resource_id', $7::text, 'status', 'completed'))
	`, organizationID, workspaceID, requestedBy, deletionTitle(resourceType), jobID, resourceType, resourceID); err != nil {
		return fmt.Errorf("create deletion notification: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE content.deletion_jobs SET status = 'completed', completed_at = now(), error_code = NULL, error_summary = NULL WHERE id = $1::uuid`, jobID); err != nil {
		return fmt.Errorf("complete deletion job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit deletion job: %w", err)
	}
	return nil
}

func processAssetDeletion(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, assetID string) error {
	result, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET deleted_at = COALESCE(deleted_at, now()), publication_status = 'archived',
		    current_published_version_id = NULL, updated_at = now()
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
	`, organizationID, workspaceID, assetID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("asset not found")
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_reviews SET status = 'superseded', reviewed_at = now() WHERE organization_id = $1::uuid AND asset_id = $2::uuid AND status = 'pending'`, organizationID, assetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE retrieval.chunk_embeddings SET status = 'deleted', updated_at = now() WHERE chunk_id IN (SELECT id FROM retrieval.chunks WHERE organization_id = $1::uuid AND asset_id = $2::uuid)`, organizationID, assetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE retrieval.chunks SET status = 'deleted', search_text = '', content = '', updated_at = now() WHERE organization_id = $1::uuid AND asset_id = $2::uuid`, organizationID, assetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE retrieval.projection_runs SET status = 'stale', updated_at = now() WHERE organization_id = $1::uuid AND asset_version_id IN (SELECT id FROM asset.asset_versions WHERE asset_id = $2::uuid) AND status <> 'stale'`, organizationID, assetID); err != nil {
		return err
	}
	return nil
}

func processWorkspaceDeletion(ctx context.Context, tx pgx.Tx, organizationID, workspaceID string) error {
	result, err := tx.Exec(ctx, `UPDATE content.workspaces SET status = 'archived', updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, organizationID, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("workspace not found")
	}
	return nil
}

func failJob(ctx context.Context, tx pgx.Tx, jobID, code string, cause error) error {
	summary := cause.Error()
	if len(summary) > 2000 {
		summary = summary[:2000]
	}
	if _, err := tx.Exec(ctx, `UPDATE content.deletion_jobs SET status = 'failed', error_code = $2, error_summary = $3, completed_at = now() WHERE id = $1::uuid`, jobID, code, summary); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func deletionTitle(resourceType string) string {
	if resourceType == "workspace" {
		return "Workspace deletion completed"
	}
	return "Asset deletion completed"
}
