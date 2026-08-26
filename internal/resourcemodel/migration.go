package resourcemodel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"github.com/jackc/pgx/v5"
)

type MigrationInput struct {
	SourceVersionID string         `json:"source_version_id"`
	TargetVersionID string         `json:"target_version_id"`
	AssetScope      map[string]any `json:"asset_scope"`
	Mapping         map[string]any `json:"mapping"`
	Defaults        map[string]any `json:"defaults"`
}

type Migration struct {
	ID              string         `json:"id"`
	WorkspaceID     string         `json:"workspace_id"`
	ResourceModelID string         `json:"resource_model_id"`
	FromVersionID   *string        `json:"from_version_id,omitempty"`
	ToVersionID     string         `json:"to_version_id"`
	Status          string         `json:"status"`
	Preview         map[string]any `json:"preview"`
	InputSnapshot   map[string]any `json:"input_snapshot"`
	ErrorSummary    *string        `json:"error_summary,omitempty"`
	CreatedBy       string         `json:"created_by"`
	CreatedAt       time.Time      `json:"created_at"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
}

func (s Service) migrationVersions(ctx context.Context, principal auth.Principal, modelID string, input MigrationInput) (Model, Version, Version, error) {
	model, err := s.Get(ctx, principal, modelID)
	if err != nil {
		return Model{}, Version{}, Version{}, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, model.ID, "model.manage"); err != nil {
		return Model{}, Version{}, Version{}, err
	}
	if !validID(input.TargetVersionID) || input.TargetVersionID == input.SourceVersionID {
		return Model{}, Version{}, Version{}, ErrInvalidInput
	}
	target, err := s.GetVersion(ctx, principal, input.TargetVersionID)
	if err != nil || target.ResourceModelID != model.ID {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Model{}, Version{}, Version{}, err
		}
		return Model{}, Version{}, Version{}, ErrInvalidInput
	}
	var source Version
	if input.SourceVersionID != "" {
		if !validID(input.SourceVersionID) {
			return Model{}, Version{}, Version{}, ErrInvalidInput
		}
		source, err = s.GetVersion(ctx, principal, input.SourceVersionID)
		if err != nil || source.ResourceModelID != model.ID {
			return Model{}, Version{}, Version{}, ErrInvalidInput
		}
	}
	return model, source, target, nil
}

func migrationPreview(source, target Version) map[string]any {
	removed, added := 0, 0
	oldFields, _ := source.FieldSchema["fields"].([]any)
	newFields, _ := target.FieldSchema["fields"].([]any)
	oldKeys := map[string]bool{}
	newKeys := map[string]bool{}
	for _, raw := range oldFields {
		if field, ok := raw.(map[string]any); ok {
			if key, ok := field["key"].(string); ok {
				oldKeys[key] = true
			}
		}
	}
	for _, raw := range newFields {
		if field, ok := raw.(map[string]any); ok {
			if key, ok := field["key"].(string); ok {
				newKeys[key] = true
			}
		}
	}
	for key := range oldKeys {
		if !newKeys[key] {
			removed++
		}
	}
	for key := range newKeys {
		if !oldKeys[key] {
			added++
		}
	}
	return map[string]any{"affected_assets": 0, "auto_migratable": 0, "failed": 0, "field_added": added, "field_removed": removed, "type_conversion_failures": 0, "defaults_used": 0, "reindex_required": true}
}

func (s Service) PreviewMigration(ctx context.Context, principal auth.Principal, modelID string, input MigrationInput) (map[string]any, error) {
	model, source, target, err := s.migrationVersions(ctx, principal, modelID, input)
	if err != nil {
		return nil, err
	}
	var affected int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM asset.assets WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND resource_model_id = $3::uuid AND publication_status <> 'archived'`, principal.OrganizationID, model.WorkspaceID, model.ID).Scan(&affected); err != nil {
		return nil, fmt.Errorf("count migration assets: %w", err)
	}
	preview := migrationPreview(source, target)
	preview["affected_assets"] = affected
	preview["auto_migratable"] = affected
	return preview, nil
}

func (s Service) StartMigration(ctx context.Context, principal auth.Principal, modelID, idempotencyKey string, input MigrationInput) (Migration, error) {
	if len(strings.TrimSpace(idempotencyKey)) < 16 {
		return Migration{}, ErrInvalidInput
	}
	model, source, target, err := s.migrationVersions(ctx, principal, modelID, input)
	if err != nil {
		return Migration{}, err
	}
	preview, err := s.PreviewMigration(ctx, principal, modelID, input)
	if err != nil {
		return Migration{}, err
	}
	snapshot := map[string]any{"source_version_id": input.SourceVersionID, "target_version_id": input.TargetVersionID, "asset_scope": input.AssetScope, "mapping": input.Mapping, "defaults": input.Defaults}
	var migrationID string
	if err := s.Store.Pool.QueryRow(ctx, `INSERT INTO model.resource_model_migrations (organization_id, workspace_id, resource_model_id, from_version_id, to_version_id, status, preview, input_snapshot, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4, '')::uuid, $5::uuid, 'queued', $6::jsonb, $7::jsonb, $8::uuid) RETURNING id::text`, principal.OrganizationID, model.WorkspaceID, model.ID, source.ID, target.ID, mustJSON(preview), mustJSON(snapshot), principal.UserID).Scan(&migrationID); err != nil {
		return Migration{}, fmt.Errorf("create resource model migration: %w", err)
	}
	return s.GetMigration(ctx, principal, migrationID)
}

func (s Service) GetMigration(ctx context.Context, principal auth.Principal, migrationID string) (Migration, error) {
	if !validID(migrationID) {
		return Migration{}, ErrInvalidInput
	}
	var modelID, workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT resource_model_id::text, workspace_id::text FROM model.resource_model_migrations WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, migrationID).Scan(&modelID, &workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Migration{}, ErrNotFound
		}
		return Migration{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, "model.manage"); err != nil {
		return Migration{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, resource_model_id::text, from_version_id::text, to_version_id::text, status, preview, input_snapshot, error_summary, created_by::text, created_at, completed_at FROM model.resource_model_migrations WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, migrationID)
	return scanMigration(row)
}

func (s Service) CancelMigration(ctx context.Context, principal auth.Principal, migrationID string) (Migration, error) {
	migration, err := s.GetMigration(ctx, principal, migrationID)
	if err != nil {
		return Migration{}, err
	}
	if migration.Status != "queued" && migration.Status != "previewing" && migration.Status != "processing" {
		return Migration{}, ErrConflict
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE model.resource_model_migrations SET status = 'cancelled', completed_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, migrationID); err != nil {
		return Migration{}, err
	}
	return s.GetMigration(ctx, principal, migrationID)
}

func scanMigration(row rowScanner) (Migration, error) {
	var item Migration
	var preview, snapshot []byte
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.ResourceModelID, &item.FromVersionID, &item.ToVersionID, &item.Status, &preview, &snapshot, &item.ErrorSummary, &item.CreatedBy, &item.CreatedAt, &item.CompletedAt)
	item.Preview, item.InputSnapshot = decodeMap(preview), decodeMap(snapshot)
	return item, err
}
