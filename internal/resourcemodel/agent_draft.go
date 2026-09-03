package resourcemodel

// agent_draft.go — the agent-facing model draft channel (docs/数据模型自由定
// 制实施方案-2026-09-03.md §5.3). Member lifecycle methods stay the single
// publish authority: these two methods only ever write draft models and draft
// model versions, never validate/publish/retire. Authorization is an explicit
// AgentAccessPolicy row carrying model.manage for the run workspace — the
// authz agent action allowlist is deliberately untouched, so the member
// surface keeps its "agents never manage models" default.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

// AgentDraftService is the agent-side write surface for model drafts. It
// shares the Store with Service but none of its member-only guards.
type AgentDraftService struct {
	Store *store.Store
}

// AgentCreateDraft mirrors Service.Create's persistence (draft model + draft
// version 1) behind the agent policy gate. Reuse of the same SQL keeps the
// member and agent rows indistinguishable downstream.
func (s AgentDraftService) AgentCreateDraft(ctx context.Context, principal auth.Principal, workspaceID string, input CreateInput) (Model, error) {
	if err := s.validateAgent(ctx, principal, workspaceID); err != nil {
		return Model{}, err
	}
	input.ModelKey = strings.TrimSpace(input.ModelKey)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if !validModelKey(input.ModelKey) || input.Name == "" {
		return Model{}, ErrInvalidInput
	}
	checksum, err := SchemaChecksum(input.ContentKind, input.InitialVersion.FieldSchema, input.InitialVersion.FormSchema, input.InitialVersion.ListSchema, input.InitialVersion.Policy)
	if err != nil {
		return Model{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Model{}, fmt.Errorf("begin agent model draft: %w", err)
	}
	defer tx.Rollback(ctx)
	var modelID, versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, description, content_kind, status, model_capabilities, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, 'draft', '{}'::jsonb, $7::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, input.ModelKey, input.Name, input.Description, input.ContentKind, principal.UserID).Scan(&modelID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Model{}, ErrConflict
		}
		return Model{}, fmt.Errorf("create agent model draft: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO model.resource_model_versions
			(organization_id, resource_model_id, version_no, status, field_schema, form_schema, list_schema, policy, schema_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, 1, 'draft', $3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8::uuid)
		RETURNING id::text
	`, principal.OrganizationID, modelID, mustJSON(input.InitialVersion.FieldSchema), mustJSON(input.InitialVersion.FormSchema), mustJSON(input.InitialVersion.ListSchema), mustJSON(input.InitialVersion.Policy), checksum, principal.UserID).Scan(&versionID); err != nil {
		return Model{}, fmt.Errorf("create agent model draft version: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE model.resource_models SET current_version_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, modelID, versionID); err != nil {
		return Model{}, fmt.Errorf("set agent model draft current version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Model{}, fmt.Errorf("commit agent model draft: %w", err)
	}
	current := Version{ID: versionID, VersionNo: 1, Status: "draft", SchemaChecksum: checksum}
	return Model{ID: modelID, WorkspaceID: workspaceID, ModelKey: input.ModelKey, Name: input.Name,
		Description: input.Description, ContentKind: input.ContentKind, Status: "draft",
		CurrentVersion: &current}, nil
}

// AgentPatchDraftVersion mirrors Service.PatchVersion (draft-only, checksum
// ETag) behind the same agent policy gate, scoped to the run workspace.
func (s AgentDraftService) AgentPatchDraftVersion(ctx context.Context, principal auth.Principal, workspaceID, versionID, expectedETag string, input VersionPatchInput) (Version, error) {
	if err := s.validateAgent(ctx, principal, workspaceID); err != nil {
		return Version{}, err
	}
	var modelID string
	var version Version
	var fieldSchema, formSchema, listSchema, policy []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT v.resource_model_id::text, v.id::text, v.version_no, v.status, v.field_schema, v.form_schema, v.list_schema, v.policy,
		       v.schema_checksum, v.validated_at, v.published_at, v.retired_at, v.created_at
		FROM model.resource_model_versions v
		JOIN model.resource_models m ON m.organization_id = v.organization_id AND m.id = v.resource_model_id
		WHERE v.organization_id = $1::uuid AND v.id = $2::uuid AND m.workspace_id = $3::uuid
	`, principal.OrganizationID, versionID, workspaceID).Scan(&modelID, &version.ID, &version.VersionNo, &version.Status,
		&fieldSchema, &formSchema, &listSchema, &policy, &version.SchemaChecksum, &version.ValidatedAt, &version.PublishedAt, &version.RetiredAt, &version.CreatedAt)
	if err != nil {
		return Version{}, ErrNotFound
	}
	if version.Status != "draft" {
		return Version{}, fmt.Errorf("%w: only draft versions can be edited", ErrConflict)
	}
	if strings.Trim(strings.TrimSpace(expectedETag), "\"") != strings.Trim(version.SchemaChecksum, "\"") {
		return Version{}, fmt.Errorf("%w: version etag mismatch", ErrConflict)
	}
	var contentKind string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT content_kind FROM model.resource_models WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, modelID).Scan(&contentKind); err != nil {
		return Version{}, err
	}
	nextField, nextForm, nextList, nextPolicy := decodeMap(fieldSchema), decodeMap(formSchema), decodeMap(listSchema), decodeMap(policy)
	if input.FieldSchema != nil {
		nextField = *input.FieldSchema
	}
	if input.FormSchema != nil {
		nextForm = *input.FormSchema
	}
	if input.ListSchema != nil {
		nextList = *input.ListSchema
	}
	if input.Policy != nil {
		nextPolicy = *input.Policy
	}
	checksum, err := SchemaChecksum(contentKind, nextField, nextForm, nextList, nextPolicy)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE model.resource_model_versions SET field_schema = $3::jsonb, form_schema = $4::jsonb, list_schema = $5::jsonb, policy = $6::jsonb, schema_checksum = $7, validated_at = NULL WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'draft'`, principal.OrganizationID, versionID, mustJSON(nextField), mustJSON(nextForm), mustJSON(nextList), mustJSON(nextPolicy), checksum); err != nil {
		return Version{}, fmt.Errorf("patch agent model draft version: %w", err)
	}
	return Version{ID: version.ID, ResourceModelID: modelID, VersionNo: version.VersionNo, Status: "draft",
		FieldSchema: nextField, FormSchema: nextForm, ListSchema: nextList, Policy: nextPolicy, SchemaChecksum: checksum}, nil
}

// validateAgent is the agent gate: an agent technical identity plus an
// explicit AgentAccessPolicy row carrying model.manage for this workspace.
// The authz Require path stays member-only on purpose.
func (s AgentDraftService) validateAgent(ctx context.Context, principal auth.Principal, workspaceID string) error {
	if principal.UserType != "agent" || strings.TrimSpace(principal.UserID) == "" || strings.TrimSpace(principal.OrganizationID) == "" || !validID(workspaceID) {
		return ErrForbidden
	}
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var granted bool
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM content.agent_access_policies
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND agent_user_id = $3::uuid AND 'model.manage' = ANY(actions)
		)
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&granted); err != nil {
		return fmt.Errorf("verify agent model.manage grant: %w", err)
	}
	if !granted {
		return ErrForbidden
	}
	return nil
}
