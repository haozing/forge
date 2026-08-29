package resourcemodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid resource model input")
	ErrForbidden    = errors.New("resource model access denied")
	ErrNotFound     = errors.New("resource model not found")
	ErrConflict     = errors.New("resource model conflict")
)

type Service struct {
	Store  *store.Store
	Policy authz.WorkspacePolicy
	Events *eventing.EventStore
}

type Model struct {
	ID                string         `json:"id"`
	WorkspaceID       string         `json:"workspace_id"`
	ModelKey          string         `json:"model_key"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	ContentKind       string         `json:"content_kind"`
	Status            string         `json:"status"`
	CurrentVersion    *Version       `json:"current_version,omitempty"`
	ModelCapabilities map[string]any `json:"model_capabilities"`
	AllowedActions    []string       `json:"allowed_actions"`
	MemberRole        string         `json:"member_role"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type Version struct {
	ID              string         `json:"id"`
	ResourceModelID string         `json:"resource_model_id"`
	VersionNo       int            `json:"version_no"`
	Status          string         `json:"status"`
	FieldSchema     map[string]any `json:"field_schema"`
	FormSchema      map[string]any `json:"form_schema"`
	ListSchema      map[string]any `json:"list_schema"`
	Policy          map[string]any `json:"policy"`
	SchemaChecksum  string         `json:"schema_checksum"`
	ValidatedAt     *time.Time     `json:"validated_at,omitempty"`
	PublishedAt     *time.Time     `json:"published_at,omitempty"`
	RetiredAt       *time.Time     `json:"retired_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

type CreateInput struct {
	ModelKey       string         `json:"model_key"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	ContentKind    string         `json:"content_kind"`
	InitialVersion InitialVersion `json:"initial_version"`
}

type InitialVersion struct {
	FieldSchema map[string]any `json:"field_schema"`
	FormSchema  map[string]any `json:"form_schema"`
	ListSchema  map[string]any `json:"list_schema"`
	Policy      map[string]any `json:"policy"`
}

type PatchInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

type VersionInput struct {
	FieldSchema map[string]any `json:"field_schema"`
	FormSchema  map[string]any `json:"form_schema"`
	ListSchema  map[string]any `json:"list_schema"`
	Policy      map[string]any `json:"policy"`
}

type VersionPatchInput struct {
	FieldSchema *map[string]any `json:"field_schema"`
	FormSchema  *map[string]any `json:"form_schema"`
	ListSchema  *map[string]any `json:"list_schema"`
	Policy      *map[string]any `json:"policy"`
}

func (s Service) validate(principal auth.Principal) error {
	if principal.UserType != "member" || strings.TrimSpace(principal.UserID) == "" || strings.TrimSpace(principal.OrganizationID) == "" {
		return ErrForbidden
	}
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	return nil
}

func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID, modelID, action string) (authz.Scope, error) {
	if err := s.validate(principal); err != nil {
		return authz.Scope{}, err
	}
	if s.Policy == nil {
		return authz.Scope{}, ErrForbidden
	}
	scope, err := s.Policy.Require(ctx, principal, workspaceID, modelID, action)
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return authz.Scope{}, ErrForbidden
	}
	return scope, err
}

func (s Service) List(ctx context.Context, principal auth.Principal, workspaceID string) ([]Model, error) {
	scope, err := s.require(ctx, principal, workspaceID, "", "model.read")
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT rm.id::text, rm.workspace_id::text, rm.model_key, rm.name, rm.description,
		       rm.content_kind, rm.status, rm.model_capabilities, rm.created_at, rm.updated_at,
		       mv.id::text, mv.version_no, mv.status, mv.field_schema, mv.form_schema,
		       mv.list_schema, mv.policy, mv.schema_checksum, mv.validated_at, mv.published_at, mv.retired_at, mv.created_at
		FROM model.resource_models rm
		LEFT JOIN model.resource_model_versions mv ON mv.organization_id = rm.organization_id AND mv.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.workspace_id = $2::uuid AND rm.status <> 'archived'
		ORDER BY rm.updated_at DESC, rm.id
	`, principal.OrganizationID, scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list resource models: %w", err)
	}
	defer rows.Close()
	items := make([]Model, 0)
	for rows.Next() {
		item, err := scanModel(rows, scope)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resource models: %w", err)
	}
	return items, nil
}

func (s Service) Get(ctx context.Context, principal auth.Principal, modelID string) (Model, error) {
	if !validID(modelID) {
		return Model{}, ErrInvalidInput
	}
	if err := s.validate(principal); err != nil {
		return Model{}, err
	}
	var workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT COALESCE(workspace_id::text, '') FROM model.resource_models WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, modelID).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, ErrNotFound
		}
		return Model{}, fmt.Errorf("load resource model workspace: %w", err)
	}
	if workspaceID == "" {
		return Model{}, ErrNotFound
	}
	scope, err := s.require(ctx, principal, workspaceID, modelID, "model.read")
	if err != nil {
		return Model{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT rm.id::text, rm.workspace_id::text, rm.model_key, rm.name, rm.description,
		       rm.content_kind, rm.status, rm.model_capabilities, rm.created_at, rm.updated_at,
		       mv.id::text, mv.version_no, mv.status, mv.field_schema, mv.form_schema,
		       mv.list_schema, mv.policy, mv.schema_checksum, mv.validated_at, mv.published_at, mv.retired_at, mv.created_at
		FROM model.resource_models rm
		LEFT JOIN model.resource_model_versions mv ON mv.organization_id = rm.organization_id AND mv.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.id = $2::uuid AND rm.workspace_id = $3::uuid
	`, principal.OrganizationID, modelID, workspaceID)
	item, err := scanModel(row, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	return item, err
}

func (s Service) Create(ctx context.Context, principal auth.Principal, workspaceID string, input CreateInput) (Model, error) {
	_, err := s.require(ctx, principal, workspaceID, "", "model.manage")
	if err != nil {
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
		return Model{}, fmt.Errorf("begin resource model create: %w", err)
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
		return Model{}, fmt.Errorf("create resource model: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO model.resource_model_versions
			(organization_id, resource_model_id, version_no, status, field_schema, form_schema, list_schema, policy, schema_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, 1, 'draft', $3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8::uuid)
		RETURNING id::text
	`, principal.OrganizationID, modelID, mustJSON(input.InitialVersion.FieldSchema), mustJSON(input.InitialVersion.FormSchema), mustJSON(input.InitialVersion.ListSchema), mustJSON(input.InitialVersion.Policy), checksum, principal.UserID).Scan(&versionID); err != nil {
		return Model{}, fmt.Errorf("create resource model version: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE model.resource_models SET current_version_id = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, modelID, versionID); err != nil {
		return Model{}, fmt.Errorf("set resource model current version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Model{}, fmt.Errorf("commit resource model create: %w", err)
	}
	return s.Get(ctx, principal, modelID)
}

func (s Service) Patch(ctx context.Context, principal auth.Principal, modelID string, input PatchInput) (Model, error) {
	if !validID(modelID) {
		return Model{}, ErrInvalidInput
	}
	model, err := s.Get(ctx, principal, modelID)
	if err != nil {
		return Model{}, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, modelID, "model.manage"); err != nil {
		return Model{}, err
	}
	name, description, status := model.Name, model.Description, model.Status
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if input.Description != nil {
		description = *input.Description
	}
	if input.Status != nil {
		status = strings.TrimSpace(*input.Status)
	}
	if name == "" || (status != "draft" && status != "active" && status != "archived") {
		return Model{}, ErrInvalidInput
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE model.resource_models SET name = $3, description = $4, status = $5, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND workspace_id = $6::uuid
	`, principal.OrganizationID, modelID, name, description, status, model.WorkspaceID); err != nil {
		return Model{}, fmt.Errorf("update resource model: %w", err)
	}
	return s.Get(ctx, principal, modelID)
}

func (s Service) Versions(ctx context.Context, principal auth.Principal, modelID string) ([]Version, error) {
	model, err := s.Get(ctx, principal, modelID)
	if err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, modelID, "model.read"); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, resource_model_id::text, version_no, status, field_schema, form_schema, list_schema, policy,
		       schema_checksum, validated_at, published_at, retired_at, created_at
		FROM model.resource_model_versions WHERE organization_id = $1::uuid AND resource_model_id = $2::uuid ORDER BY version_no DESC
	`, principal.OrganizationID, modelID)
	if err != nil {
		return nil, fmt.Errorf("list resource model versions: %w", err)
	}
	defer rows.Close()
	items := make([]Version, 0)
	for rows.Next() {
		item, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) CreateVersion(ctx context.Context, principal auth.Principal, modelID string, input VersionInput) (Version, error) {
	model, err := s.Get(ctx, principal, modelID)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, modelID, "model.manage"); err != nil {
		return Version{}, err
	}
	checksum, err := SchemaChecksum(model.ContentKind, input.FieldSchema, input.FormSchema, input.ListSchema, input.Policy)
	if err != nil {
		return Version{}, err
	}
	var result Version
	var fieldSchema, formSchema, listSchema, policy []byte
	err = s.Store.Pool.QueryRow(ctx, `
		WITH next_version AS (SELECT COALESCE(max(version_no), 0) + 1 AS version_no FROM model.resource_model_versions WHERE organization_id = $1::uuid AND resource_model_id = $2::uuid)
		INSERT INTO model.resource_model_versions (organization_id, resource_model_id, version_no, status, field_schema, form_schema, list_schema, policy, schema_checksum, created_by)
		SELECT $1::uuid, $2::uuid, version_no, 'draft', $3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8::uuid FROM next_version
		RETURNING id::text, resource_model_id::text, version_no, status, field_schema, form_schema, list_schema, policy, schema_checksum, validated_at, published_at, retired_at, created_at
	`, principal.OrganizationID, modelID, mustJSON(input.FieldSchema), mustJSON(input.FormSchema), mustJSON(input.ListSchema), mustJSON(input.Policy), checksum, principal.UserID).Scan(&result.ID, &result.ResourceModelID, &result.VersionNo, &result.Status, &fieldSchema, &formSchema, &listSchema, &policy, &result.SchemaChecksum, &result.ValidatedAt, &result.PublishedAt, &result.RetiredAt, &result.CreatedAt)
	if err != nil {
		return Version{}, fmt.Errorf("create resource model version: %w", err)
	}
	result.FieldSchema, result.FormSchema, result.ListSchema, result.Policy = decodeMap(fieldSchema), decodeMap(formSchema), decodeMap(listSchema), decodeMap(policy)
	return result, nil
}

func (s Service) PatchVersion(ctx context.Context, principal auth.Principal, versionID, expectedETag string, input VersionPatchInput) (Version, error) {
	version, err := s.GetVersion(ctx, principal, versionID)
	if err != nil {
		return Version{}, err
	}
	model, err := s.Get(ctx, principal, version.ResourceModelID)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, model.ID, "model.manage"); err != nil {
		return Version{}, err
	}
	if version.Status != "draft" {
		return Version{}, fmt.Errorf("%w: only draft versions can be edited", ErrConflict)
	}
	if strings.Trim(strings.TrimSpace(expectedETag), "\"") != strings.Trim(version.SchemaChecksum, "\"") {
		return Version{}, fmt.Errorf("%w: version etag mismatch", ErrConflict)
	}
	fieldSchema, formSchema, listSchema, policy := version.FieldSchema, version.FormSchema, version.ListSchema, version.Policy
	if input.FieldSchema != nil {
		fieldSchema = *input.FieldSchema
	}
	if input.FormSchema != nil {
		formSchema = *input.FormSchema
	}
	if input.ListSchema != nil {
		listSchema = *input.ListSchema
	}
	if input.Policy != nil {
		policy = *input.Policy
	}
	checksum, err := SchemaChecksum(model.ContentKind, fieldSchema, formSchema, listSchema, policy)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE model.resource_model_versions SET field_schema = $3::jsonb, form_schema = $4::jsonb, list_schema = $5::jsonb, policy = $6::jsonb, schema_checksum = $7, validated_at = NULL WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'draft'`, principal.OrganizationID, versionID, mustJSON(fieldSchema), mustJSON(formSchema), mustJSON(listSchema), mustJSON(policy), checksum); err != nil {
		return Version{}, fmt.Errorf("patch resource model version: %w", err)
	}
	return s.GetVersion(ctx, principal, versionID)
}

func (s Service) GetVersion(ctx context.Context, principal auth.Principal, versionID string) (Version, error) {
	if !validID(versionID) {
		return Version{}, ErrInvalidInput
	}
	var modelID, workspaceID string
	err := s.Store.Pool.QueryRow(ctx, `SELECT rm.id::text, COALESCE(rm.workspace_id::text, '') FROM model.resource_model_versions mv JOIN model.resource_models rm ON rm.organization_id = mv.organization_id AND rm.id = mv.resource_model_id WHERE mv.id = $1::uuid AND mv.organization_id = $2::uuid`, versionID, principal.OrganizationID).Scan(&modelID, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) || workspaceID == "" {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("load resource model version scope: %w", err)
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, "model.read"); err != nil {
		return Version{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `SELECT id::text, resource_model_id::text, version_no, status, field_schema, form_schema, list_schema, policy, schema_checksum, validated_at, published_at, retired_at, created_at FROM model.resource_model_versions WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, versionID)
	return scanVersion(row)
}

func (s Service) ValidateVersion(ctx context.Context, principal auth.Principal, versionID string) (Version, error) {
	version, err := s.GetVersion(ctx, principal, versionID)
	if err != nil {
		return Version{}, err
	}
	model, err := s.Get(ctx, principal, version.ResourceModelID)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, model.ID, "model.manage"); err != nil {
		return Version{}, err
	}
	checksum, err := SchemaChecksum(model.ContentKind, version.FieldSchema, version.FormSchema, version.ListSchema, version.Policy)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE model.resource_model_versions SET schema_checksum = $3, validated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'draft'`, principal.OrganizationID, versionID, checksum); err != nil {
		return Version{}, fmt.Errorf("validate resource model version: %w", err)
	}
	return s.GetVersion(ctx, principal, versionID)
}

func (s Service) PublishVersion(ctx context.Context, principal auth.Principal, versionID string) (Version, error) {
	version, err := s.GetVersion(ctx, principal, versionID)
	if err != nil {
		return Version{}, err
	}
	model, err := s.Get(ctx, principal, version.ResourceModelID)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, model.ID, "model.manage"); err != nil {
		return Version{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("begin resource model publish: %w", err)
	}
	defer tx.Rollback(ctx)
	var validatedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT validated_at FROM model.resource_model_versions WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE`, principal.OrganizationID, versionID).Scan(&validatedAt); err != nil {
		return Version{}, fmt.Errorf("lock resource model version: %w", err)
	}
	if validatedAt == nil {
		return Version{}, fmt.Errorf("%w: version must be validated", ErrConflict)
	}
	if _, err := tx.Exec(ctx, `UPDATE model.resource_model_versions SET status = 'retired', retired_at = now() WHERE organization_id = $1::uuid AND resource_model_id = $2::uuid AND status = 'published' AND id <> $3::uuid`, principal.OrganizationID, model.ID, versionID); err != nil {
		return Version{}, fmt.Errorf("retire previous resource model version: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE model.resource_model_versions SET status = 'published', published_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, versionID); err != nil {
		return Version{}, fmt.Errorf("publish resource model version: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE model.resource_models SET current_version_id = $3::uuid, status = 'active', updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid AND workspace_id = $4::uuid`, principal.OrganizationID, model.ID, versionID, model.WorkspaceID); err != nil {
		return Version{}, fmt.Errorf("set current resource model version: %w", err)
	}
	if s.Events == nil {
		return Version{}, errors.New("event store is not initialized")
	}
	if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      model.WorkspaceID,
		EventType:        eventing.EventResourceModelPolicyPublished,
		AggregateType:    "resource_model",
		AggregateID:      model.ID,
		AggregateVersion: 1,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload: eventing.ResourceModelPolicyPublishedPayload{
			ResourceModelID: model.ID,
			VersionID:       versionID,
			WorkspaceID:     model.WorkspaceID,
		},
	}); err != nil {
		return Version{}, fmt.Errorf("record resource model policy published event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Version{}, fmt.Errorf("commit resource model publish: %w", err)
	}
	return s.GetVersion(ctx, principal, versionID)
}

func (s Service) RetireVersion(ctx context.Context, principal auth.Principal, versionID string) (Version, error) {
	version, err := s.GetVersion(ctx, principal, versionID)
	if err != nil {
		return Version{}, err
	}
	model, err := s.Get(ctx, principal, version.ResourceModelID)
	if err != nil {
		return Version{}, err
	}
	if _, err := s.require(ctx, principal, model.WorkspaceID, model.ID, "model.manage"); err != nil {
		return Version{}, err
	}
	if version.Status != "published" {
		return Version{}, fmt.Errorf("%w: only published versions can be retired", ErrConflict)
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE model.resource_model_versions
		SET status = 'retired', retired_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'published'
	`, principal.OrganizationID, versionID); err != nil {
		return Version{}, fmt.Errorf("retire resource model version: %w", err)
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE model.resource_models
		SET current_version_id = NULL, status = 'draft', updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND current_version_id = $3::uuid
	`, principal.OrganizationID, model.ID, versionID); err != nil {
		return Version{}, fmt.Errorf("clear retired resource model current version: %w", err)
	}
	return s.GetVersion(ctx, principal, versionID)
}

type rowScanner interface{ Scan(...any) error }

func scanModel(row rowScanner, scope authz.Scope) (Model, error) {
	var item Model
	var capabilities []byte
	var versionID *string
	var versionNo *int
	var versionStatus *string
	var fieldSchema, formSchema, listSchema, policy []byte
	var checksum *string
	var validatedAt, publishedAt, retiredAt *time.Time
	var versionCreatedAt *time.Time
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.ModelKey, &item.Name, &item.Description, &item.ContentKind, &item.Status, &capabilities, &item.CreatedAt, &item.UpdatedAt,
		&versionID, &versionNo, &versionStatus, &fieldSchema, &formSchema, &listSchema, &policy, &checksum, &validatedAt, &publishedAt, &retiredAt, &versionCreatedAt)
	if err != nil {
		return Model{}, err
	}
	item.ModelCapabilities = decodeMap(capabilities)
	item.AllowedActions = scope.AllowedActions
	item.MemberRole = scope.Role
	if versionID != nil {
		item.CurrentVersion = &Version{ID: *versionID, ResourceModelID: item.ID, VersionNo: derefInt(versionNo), Status: derefString(versionStatus), FieldSchema: decodeMap(fieldSchema), FormSchema: decodeMap(formSchema), ListSchema: decodeMap(listSchema), Policy: decodeMap(policy), SchemaChecksum: derefString(checksum), ValidatedAt: validatedAt, PublishedAt: publishedAt, RetiredAt: retiredAt, CreatedAt: derefTime(versionCreatedAt)}
	}
	return item, nil
}

func scanVersion(row rowScanner) (Version, error) {
	var result Version
	var fieldSchema, formSchema, listSchema, policy []byte
	err := row.Scan(&result.ID, &result.ResourceModelID, &result.VersionNo, &result.Status, &fieldSchema, &formSchema, &listSchema, &policy, &result.SchemaChecksum, &result.ValidatedAt, &result.PublishedAt, &result.RetiredAt, &result.CreatedAt)
	result.FieldSchema, result.FormSchema, result.ListSchema, result.Policy = decodeMap(fieldSchema), decodeMap(formSchema), decodeMap(listSchema), decodeMap(policy)
	return result, err
}

func decodeMap(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

func mustJSON(value any) []byte { body, _ := json.Marshal(value); return body }
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func validModelKey(value string) bool {
	if len(value) < 2 || len(value) > 80 {
		return false
	}
	for index, char := range value {
		if index == 0 {
			if char < 'a' || char > 'z' {
				return false
			}
			continue
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return false
	}
	return true
}
