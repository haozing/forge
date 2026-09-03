package asset

// write.go — the Agent/OpenAPI write channel. Create materializes the asset,
// its first sealed version and the clean shared draft in one transaction;
// Update is a draft autosave (revision bump only, never a version — commits
// go through the draft commit path). Idempotency, field validation and
// reference checks are shared with the webhook and import channels.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/tag"

	"github.com/jackc/pgx/v5"
)

var ErrIdempotencyConflict = errors.New("idempotency key conflict")

type AssetResult struct {
	ID                        string         `json:"id"`
	ResourceModelID           string         `json:"resource_model_id"`
	WorkspaceID               string         `json:"workspace_id"`
	CurrentWorkingVersionID   string         `json:"current_working_version_id"`
	CurrentPublishedVersionID *string        `json:"current_published_version_id"`
	PublicationStatus         string         `json:"publication_status"`
	Title                     *string        `json:"title"`
	Markdown                  *string        `json:"markdown,omitempty"`
	Fields                    map[string]any `json:"fields"`
	UpdatedAt                 time.Time      `json:"updated_at"`
}

type CreateInput struct {
	ResourceModelID string
	// WorkspaceID is required when the model is organization-level (builtin
	// models ship with NULL workspace): agents have no workspace identity of
	// their own, so the caller names the target workspace explicitly.
	WorkspaceID string
	Title       *string
	Markdown    *string
	Fields      map[string]any
	// TagIDs attaches existing workspace tags to the first version; nil means
	// none. Tags keep TagService rules (workspace identity, active-only,
	// MaxTagsPerDraft) — agents cannot create tags implicitly.
	TagIDs []string
	// Source carries optional channel provenance. It is preserved in the
	// raw_inputs payload only; version snapshots record no channel JSON.
	Source map[string]any
}

type UpdateInput struct {
	Title    *string
	Markdown *string
	Fields   *map[string]any
	// TagIDs replaces the draft's tag selection when non-nil (an empty slice
	// clears it); nil leaves tags untouched.
	TagIDs *[]string
}

func (s Service) Create(ctx context.Context, principal auth.Principal, allowedModelIDs []string, idempotencyKey string, input CreateInput) (AssetResult, error) {
	if len(allowedModelIDs) == 0 || !validID(input.ResourceModelID) || !validIdempotencyKey(idempotencyKey) {
		return AssetResult{}, ErrInvalidInput
	}
	if err := validateContent(input.Title, input.Markdown, &input.Fields); err != nil {
		return AssetResult{}, err
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AssetResult{}, errors.New("database store is not initialized")
	}
	requestHash := hashRequest("asset.create", input)
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return AssetResult{}, fmt.Errorf("begin asset create: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := beginIdempotency(ctx, tx, principal, "asset.create", idempotencyKey, requestHash)
	if err != nil {
		return AssetResult{}, err
	}
	if state.Replay {
		var result AssetResult
		if err := json.Unmarshal(state.Body, &result); err != nil {
			return AssetResult{}, fmt.Errorf("decode idempotent asset response: %w", err)
		}
		return result, nil
	}
	fields := input.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	contentChecksum := hashRequest("asset.content", struct {
		Title    *string        `json:"title"`
		Markdown *string        `json:"markdown"`
		Fields   map[string]any `json:"fields"`
	}{input.Title, input.Markdown, fields})
	rawPayload, _ := json.Marshal(struct {
		Channel  string         `json:"channel"`
		Title    *string        `json:"title"`
		Markdown *string        `json:"markdown"`
		Fields   map[string]any `json:"fields"`
		Source   map[string]any `json:"source,omitempty"`
	}{"api", input.Title, input.Markdown, fields, input.Source})
	var modelVersionID string
	var modelWorkspace *string
	var fieldSchema []byte
	if err := tx.QueryRow(ctx, `
		SELECT mv.id::text, mv.field_schema, rm.workspace_id::text
		FROM model.resource_models rm
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		WHERE rm.id = $1::uuid
		  AND rm.organization_id = $2::uuid
		  AND rm.id::text = ANY($3::text[])
		  AND rm.status = 'active'
	`, input.ResourceModelID, principal.OrganizationID, allowedModelIDs).Scan(&modelVersionID, &fieldSchema, &modelWorkspace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AssetResult{}, ErrNotFound
		}
		return AssetResult{}, fmt.Errorf("load resource model version: %w", err)
	}
	// Workspace resolution (webhook semantics): a workspace-bound model pins
	// the target; an organization-level model requires the caller to name it.
	var workspaceID string
	if modelWorkspace != nil && *modelWorkspace != "" {
		workspaceID = *modelWorkspace
		if input.WorkspaceID != "" && input.WorkspaceID != workspaceID {
			return AssetResult{}, ErrInvalidInput
		}
	} else {
		if !validID(input.WorkspaceID) {
			return AssetResult{}, ErrInvalidInput
		}
		var wsOK bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM content.workspaces
				WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
			)
		`, principal.OrganizationID, input.WorkspaceID).Scan(&wsOK); err != nil {
			return AssetResult{}, fmt.Errorf("load create target workspace: %w", err)
		}
		if !wsOK {
			return AssetResult{}, ErrInvalidInput
		}
		workspaceID = input.WorkspaceID
	}
	// Defaults enter the version snapshot only; the raw-input payload above
	// keeps exactly what the caller sent (doc §5.3 raw/materialized split).
	fields = applyDefaults(fieldSchema, fields)
	if err := validateFields(fieldSchema, fields); err != nil {
		return AssetResult{}, err
	}
	if err := validateAssetReferences(ctx, tx, principal, workspaceID, fieldSchema, fields); err != nil {
		return AssetResult{}, err
	}
	tagIDs, tagErr := resolveActiveTagIDsTx(ctx, tx, principal.OrganizationID, workspaceID, input.TagIDs)
	if tagErr != nil {
		return AssetResult{}, tagErr
	}
	var rawInputID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.raw_inputs
			(organization_id, workspace_id, submitted_by, source_type, content_type, payload, content_checksum)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'api', 'application/json', $4::jsonb, $5)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, principal.UserID, string(rawPayload), contentChecksum).Scan(&rawInputID); err != nil {
		return AssetResult{}, fmt.Errorf("record raw input: %w", err)
	}
	assetID, versionID, versionNo, err := createAssetWithFirstVersionTx(ctx, tx,
		principal.OrganizationID, workspaceID, input.ResourceModelID, modelVersionID,
		rawInputID, OriginAIGenerated, derefString(input.Title), "", derefString(input.Markdown),
		fields, tagIDs, tag.SourceAPI, principal.UserID)
	if err != nil {
		return AssetResult{}, err
	}
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return AssetResult{}, err
	}
	if err := AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetVersionCreated, eventing.PayloadVersionV1, eventing.AssetVersionCreatedPayload{
		AssetID:     assetID,
		VersionID:   versionID,
		VersionNo:   versionNo,
		WorkspaceID: workspaceID,
	}); err != nil {
		return AssetResult{}, err
	}
	RecordAssetAuditTx(ctx, tx, principal.OrganizationID, workspaceID, principal, "asset.create", assetID, map[string]any{
		"workspace_id": workspaceID,
		"version_id":   versionID,
	})
	result, err := loadAssetTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return AssetResult{}, err
	}
	if err := saveIdempotency(ctx, tx, principal, "asset.create", idempotencyKey, result, httpCreated); err != nil {
		return AssetResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AssetResult{}, fmt.Errorf("commit asset create: %w", err)
	}
	return result, nil
}

// createAssetWithFirstVersionTx is the shared tail of every "new asset"
// pipeline (API create, webhook intake, import row): it inserts the asset
// (pointers NULL, the deferred triggers tolerate this inside the create
// transaction), appends the first sealed version through CreateVersionTx,
// inserts the clean shared draft at revision 1 and links both pointers.
// tagIDs are the resolved workspace tag identities of the first snapshot;
// tagSource is their fallback provenance ("import"/"webhook"). The fresh draft
// inherits the version relations so version and draft stay consistent.
// Callers own validation, idempotency, events and audit.
func createAssetWithFirstVersionTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, resourceModelID, resourceModelVersionID, rawInputID, origin, title, summary, markdown string, fields map[string]any, tagIDs []string, tagSource, actorID string) (string, string, int64, error) {
	var assetID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4,'')::uuid)
		RETURNING id::text
	`, organizationID, workspaceID, resourceModelID, actorID).Scan(&assetID); err != nil {
		return "", "", 0, fmt.Errorf("insert asset: %w", err)
	}
	versionID, versionNo, err := CreateVersionTx(ctx, tx, VersionMaterial{
		OrganizationID:         organizationID,
		WorkspaceID:            workspaceID,
		AssetID:                assetID,
		ResourceModelID:        resourceModelID,
		ResourceModelVersionID: resourceModelVersionID,
		Origin:                 origin,
		ConfirmationStatus:     ConfirmationUnconfirmed,
		Title:                  title,
		Summary:                summary,
		Markdown:               markdown,
		Fields:                 fields,
		TagIDs:                 tagIDs,
		TagSource:              tagSource,
		SourceRawInputID:       rawInputID,
		CreatedBy:              actorID,
	})
	if err != nil {
		return "", "", 0, err
	}
	var draftID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_drafts
			(organization_id, workspace_id, asset_id, base_version_id, revision, committed_revision,
			 title, summary, markdown, fields, origin, updated_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 1, $5, $6, $7, $8::jsonb, $9, NULLIF($10,'')::uuid)
		RETURNING id::text
	`, organizationID, workspaceID, assetID, versionID, title, summary, markdown,
		string(mustJSON(fields)), origin, actorID).Scan(&draftID); err != nil {
		return "", "", 0, fmt.Errorf("insert draft: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $3::uuid, draft_id = $4::uuid
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, assetID, versionID, draftID); err != nil {
		return "", "", 0, fmt.Errorf("link asset pointers: %w", err)
	}
	// The fresh draft inherits the version's tag relations and provenance so
	// a later commit reproduces the same tag set.
	if err := initializeDraftTagsFromVersionTx(ctx, tx, organizationID, draftID, versionID); err != nil {
		return "", "", 0, err
	}
	return assetID, versionID, versionNo, nil
}

// Update autosaves the shared draft of an asset: it patches asset_drafts and
// bumps the draft revision; no AssetVersion is created. expectedRevision is
// the optimistic draft revision (If-Match); a mismatch returns
// ErrDraftRevisionMismatch.
func (s Service) Update(ctx context.Context, principal auth.Principal, allowedModelIDs []string, idempotencyKey, assetID, expectedRevision string, input UpdateInput) (AssetResult, error) {
	if len(allowedModelIDs) == 0 || !validID(assetID) || !validDraftRevision(expectedRevision) || !validIdempotencyKey(idempotencyKey) {
		return AssetResult{}, ErrInvalidInput
	}
	if input.Title == nil && input.Markdown == nil && input.Fields == nil && input.TagIDs == nil {
		return AssetResult{}, ErrInvalidInput
	}
	if err := validateContent(input.Title, input.Markdown, input.Fields); err != nil {
		return AssetResult{}, err
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AssetResult{}, errors.New("database store is not initialized")
	}
	requestHash := hashRequest("asset.update", struct {
		AssetID  string
		Revision string
		Input    UpdateInput
	}{assetID, expectedRevision, input})
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return AssetResult{}, fmt.Errorf("begin asset update: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := beginIdempotency(ctx, tx, principal, "asset.update", idempotencyKey, requestHash)
	if err != nil {
		return AssetResult{}, err
	}
	if state.Replay {
		var result AssetResult
		if err := json.Unmarshal(state.Body, &result); err != nil {
			return AssetResult{}, fmt.Errorf("decode idempotent asset response: %w", err)
		}
		return result, nil
	}
	var workspaceID, modelID, publicationStatus string
	err = tx.QueryRow(ctx, `
		SELECT a.workspace_id::text, a.resource_model_id::text, a.publication_status
		FROM asset.assets a
		WHERE a.id = $1::uuid
		  AND a.organization_id = $2::uuid
		  AND a.resource_model_id::text = ANY($3::text[])
		FOR UPDATE
	`, assetID, principal.OrganizationID, allowedModelIDs).Scan(&workspaceID, &modelID, &publicationStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssetResult{}, ErrNotFound
	}
	if err != nil {
		return AssetResult{}, fmt.Errorf("load asset for update: %w", err)
	}
	if publicationStatus == PublicationArchived {
		return AssetResult{}, ErrAssetArchived
	}
	draft, err := LoadDraftTx(ctx, tx, principal.OrganizationID, assetID, expectedRevision)
	if err != nil {
		return AssetResult{}, err
	}
	if input.Markdown != nil {
		// Conversation notes are block-managed: their markdown is a frozen
		// render, never an editable draft field (member surface guards the
		// same way via AutosaveDraft).
		if _, isNote, noteErr := noteContainerIDTx(ctx, tx, principal.OrganizationID, assetID); noteErr != nil {
			return AssetResult{}, noteErr
		} else if isNote {
			return AssetResult{}, ErrNoteBlocksManaged
		}
	}
	if input.Title != nil {
		draft.Title = strings.TrimSpace(*input.Title)
	}
	if input.Markdown != nil {
		draft.Markdown = *input.Markdown
	}
	if input.Fields != nil {
		draft.Fields = *input.Fields
	}
	if draft.Fields == nil {
		draft.Fields = map[string]any{}
	}
	var fieldSchema []byte
	if err := tx.QueryRow(ctx, `
		SELECT v.field_schema FROM model.resource_models m
		JOIN model.resource_model_versions v ON v.organization_id = m.organization_id AND v.id = m.current_version_id
		WHERE m.organization_id = $1::uuid AND m.id = $2::uuid
	`, principal.OrganizationID, modelID).Scan(&fieldSchema); err != nil {
		return AssetResult{}, fmt.Errorf("load resource model field schema: %w", err)
	}
	if err := validateFields(fieldSchema, draft.Fields); err != nil {
		return AssetResult{}, err
	}
	if err := validateAssetReferences(ctx, tx, principal, workspaceID, fieldSchema, draft.Fields); err != nil {
		return AssetResult{}, err
	}
	if input.TagIDs != nil {
		if err := replaceDraftTagIDsTx(ctx, tx, principal, workspaceID, draft, *input.TagIDs); err != nil {
			return AssetResult{}, err
		}
	}
	if err := persistDraftPatch(ctx, tx, principal.OrganizationID, draft, principal.UserID); err != nil {
		return AssetResult{}, err
	}
	result, err := loadAssetTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return AssetResult{}, err
	}
	if err := saveIdempotency(ctx, tx, principal, "asset.update", idempotencyKey, result, httpOK); err != nil {
		return AssetResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AssetResult{}, fmt.Errorf("commit asset update: %w", err)
	}
	return result, nil
}

// resolveActiveTagIDsTx validates a create-time tag selection against the
// TagService rules agents must honor: workspace identity, active status and
// the shared per-version cap. It returns the deduplicated, sorted ID list.
func resolveActiveTagIDsTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID string, tagIDs []string) ([]string, error) {
	ids := dedupeSort(tagIDs)
	if len(ids) == 0 {
		return ids, nil
	}
	if len(ids) > MaxTagsPerDraft {
		return nil, ErrTooManyTags
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, ErrInvalidInput
		}
	}
	var bad int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		  AND id = ANY($3::uuid[]) AND status <> 'active'
	`, organizationID, workspaceID, ids).Scan(&bad); err != nil {
		return nil, fmt.Errorf("verify create tags: %w", err)
	}
	if bad > 0 {
		return nil, ErrTagArchived
	}
	return ids, nil
}

// replaceDraftTagIDsTx swaps the draft's tag selection for the given ID list
// (agent surface: fixed 'api' provenance, no confidence semantics). It mirrors
// the member SetDraftTags rules — same workspace, active unless already on the
// draft — and keeps the cap uniform.
func replaceDraftTagIDsTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID string, draft Draft, tagIDs []string) error {
	ids := dedupeSort(tagIDs)
	if len(ids) > MaxTagsPerDraft {
		return ErrTooManyTags
	}
	for _, id := range ids {
		if !validID(id) {
			return ErrInvalidInput
		}
	}
	for _, id := range ids {
		var ok bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM asset.tags t
				WHERE t.organization_id = $1::uuid AND t.workspace_id = $2::uuid AND t.id = $3::uuid
				  AND (t.status = 'active'
				       OR EXISTS (SELECT 1 FROM asset.asset_draft_tags dt
					        WHERE dt.asset_draft_id = $4::uuid AND dt.tag_id = t.id))
			)
		`, principal.OrganizationID, workspaceID, id, draft.DraftID).Scan(&ok); err != nil {
			return fmt.Errorf("verify draft tag: %w", err)
		}
		if !ok {
			return ErrTagArchived
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM asset.asset_draft_tags
		WHERE organization_id = $1::uuid AND asset_draft_id = $2::uuid
		  AND NOT (tag_id = ANY($3::uuid[]))
	`, principal.OrganizationID, draft.DraftID, ids); err != nil {
		return fmt.Errorf("remove draft tags: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_draft_tags
				(organization_id, workspace_id, asset_draft_id, tag_id, source, added_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6::uuid)
			ON CONFLICT (asset_draft_id, tag_id) DO NOTHING
		`, principal.OrganizationID, workspaceID, draft.DraftID, id, tag.SourceAPI, principal.UserID); err != nil {
			return fmt.Errorf("insert draft tag: %w", err)
		}
	}
	return nil
}

// validDraftRevision reports whether value is a positive decimal draft
// revision — the If-Match token for autosave writes.
func validDraftRevision(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	_, err := strconv.ParseInt(value, 10, 63)
	return err == nil
}

type idempotencyState struct {
	Replay bool
	Body   []byte
}

func beginIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, operation, key, requestHash string) (idempotencyState, error) {
	_, _ = tx.Exec(ctx, `DELETE FROM system.idempotency_keys WHERE organization_id = $1::uuid AND subject_id = $2::uuid AND operation = $3 AND idempotency_key = $4 AND expires_at <= now()`, principal.OrganizationID, principal.UserID, operation, key)
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
		RETURNING id::text
	`, principal.OrganizationID, principal.UserID, operation, key, requestHash).Scan(&id)
	if err == nil {
		return idempotencyState{}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return idempotencyState{}, fmt.Errorf("reserve idempotency key: %w", err)
	}
	var storedHash string
	var body []byte
	err = tx.QueryRow(ctx, `
		SELECT request_hash, response_body
		FROM system.idempotency_keys
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = $3 AND idempotency_key = $4
		FOR UPDATE
	`, principal.OrganizationID, principal.UserID, operation, key).Scan(&storedHash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotencyState{}, ErrConflict
	}
	if err != nil {
		return idempotencyState{}, fmt.Errorf("load idempotency key: %w", err)
	}
	if storedHash != requestHash {
		return idempotencyState{}, ErrIdempotencyConflict
	}
	if len(body) == 0 {
		return idempotencyState{}, ErrConflict
	}
	return idempotencyState{Replay: true, Body: body}, nil
}

func saveIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, operation, key string, response any, status int) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode idempotent response: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = $5, response_body = $6::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = $3 AND idempotency_key = $4
	`, principal.OrganizationID, principal.UserID, operation, key, status, string(body)); err != nil {
		return fmt.Errorf("save idempotent response: %w", err)
	}
	return nil
}

// loadAssetTx reads the agent-facing asset projection. Content columns come
// from the shared draft; publication facts come from the asset row.
func loadAssetTx(ctx context.Context, tx pgx.Tx, organizationID, assetID string) (AssetResult, error) {
	var result AssetResult
	err := tx.QueryRow(ctx, `
		SELECT a.id::text, a.resource_model_id::text, a.workspace_id::text,
		       a.current_working_version_id::text, a.current_published_version_id::text,
		       a.publication_status, d.title, d.markdown, d.fields, a.updated_at
		FROM asset.assets a
		JOIN asset.asset_drafts d ON d.organization_id = a.organization_id AND d.asset_id = a.id
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
	`, organizationID, assetID).Scan(&result.ID, &result.ResourceModelID, &result.WorkspaceID,
		&result.CurrentWorkingVersionID, &result.CurrentPublishedVersionID,
		&result.PublicationStatus, &result.Title, &result.Markdown, &result.Fields, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssetResult{}, ErrNotFound
	}
	if err != nil {
		return AssetResult{}, fmt.Errorf("load asset result: %w", err)
	}
	if result.Fields == nil {
		result.Fields = map[string]any{}
	}
	return result, nil
}

// ValidateFields exposes the model contract to Agent candidate processors.
func ValidateFields(schemaBytes []byte, fields map[string]any) error {
	return validateFields(schemaBytes, fields)
}

// applyDefaults fills absent top-level field keys from the model schema's
// per-field default values. Explicit values — including explicit nulls, which
// are user intent — are never overridden, and defaults are deep-copied so
// drafts never alias schema JSON. The merged result still flows through
// validateFields: an invalid default fails the write wholesale.
func applyDefaults(schemaBytes []byte, fields map[string]any) map[string]any {
	if len(schemaBytes) == 0 || string(schemaBytes) == "{}" || string(schemaBytes) == "null" {
		return fields
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return fields
	}
	rawFields, ok := schema["fields"].([]any)
	if !ok {
		return fields
	}
	if fields == nil {
		fields = map[string]any{}
	}
	for _, raw := range rawFields {
		definition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := definition["key"].(string)
		defaultValue, exists := definition["default"]
		if name == "" || !exists {
			continue
		}
		if _, present := fields[name]; present {
			continue
		}
		fields[name] = deepCopyJSON(defaultValue)
	}
	return fields
}

func deepCopyJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, item := range typed {
			clone[key] = deepCopyJSON(item)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for index, item := range typed {
			clone[index] = deepCopyJSON(item)
		}
		return clone
	default:
		return typed
	}
}

// ValidateContent exposes common asset content limits to background processors.
func ValidateContent(title, markdown *string, fields *map[string]any) error {
	return validateContent(title, markdown, fields)
}

func validateContent(title, markdown *string, fields *map[string]any) error {
	if title != nil && len([]rune(*title)) > 500 {
		return ErrInvalidInput
	}
	if markdown != nil && len([]byte(*markdown)) > 2_000_000 {
		return ErrInvalidInput
	}
	if fields != nil {
		if _, err := json.Marshal(*fields); err != nil {
			return ErrInvalidInput
		}
	}
	return nil
}

func validateFields(schemaBytes []byte, fields map[string]any) error {
	if len(schemaBytes) == 0 || string(schemaBytes) == "{}" || string(schemaBytes) == "null" {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return ErrInvalidInput
	}
	properties, required, additional := schemaProperties(schema)
	if !additional {
		for key := range fields {
			if _, ok := properties[key]; !ok {
				return ErrInvalidInput
			}
		}
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok || value == nil {
			return ErrInvalidInput
		}
	}
	for key, value := range fields {
		definition, ok := properties[key].(map[string]any)
		if !ok {
			continue
		}
		if !matchesFieldDefinition(value, definition) {
			return ErrInvalidInput
		}
	}
	return nil
}

func schemaProperties(schema map[string]any) (map[string]any, []string, bool) {
	properties := map[string]any{}
	if fields, ok := schema["fields"].([]any); ok {
		for _, raw := range fields {
			field, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := field["key"].(string)
			if name != "" {
				properties[name] = field
			}
		}
	}
	required := make([]string, 0)
	for name, raw := range properties {
		definition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if requiredValue, ok := definition["required"].(bool); ok && requiredValue {
			required = append(required, name)
		}
	}
	additional := false
	if value, ok := schema["additional_properties"].(bool); ok {
		additional = value
	}
	return properties, required, additional
}

func matchesFieldDefinition(value any, definition map[string]any) bool {
	fieldType, _ := definition["type"].(string)
	if !matchesJSONType(value, fieldType) {
		return false
	}
	if fieldType == "enum" && !matchesOption(value, definition["options"]) {
		return false
	}
	if fieldType == "multiselect" {
		values, _ := value.([]any)
		for _, item := range values {
			if !matchesOption(item, definition["options"]) {
				return false
			}
		}
	}
	if fieldType == "object" {
		object, _ := value.(map[string]any)
		properties, _ := definition["properties"].(map[string]any)
		for key, item := range object {
			child, ok := properties[key].(map[string]any)
			if !ok || !matchesFieldDefinition(item, child) {
				return false
			}
		}
		for key, raw := range properties {
			child, _ := raw.(map[string]any)
			if required, _ := child["required"].(bool); required {
				if item, exists := object[key]; !exists || item == nil {
					return false
				}
			}
		}
	}
	if fieldType == "array" {
		values, _ := value.([]any)
		items, ok := definition["items"].(map[string]any)
		if !ok {
			return false
		}
		for _, item := range values {
			if !matchesFieldDefinition(item, items) {
				return false
			}
		}
	}
	if stringValue, ok := value.(string); ok {
		validation, _ := definition["validation"].(map[string]any)
		if limit, ok := numericValue(validation["min_length"]); ok && float64(len([]rune(stringValue))) < limit {
			return false
		}
		if limit, ok := numericValue(validation["max_length"]); ok && float64(len([]rune(stringValue))) > limit {
			return false
		}
	}
	if numberValue, ok := numericValue(value); ok {
		validation, _ := definition["validation"].(map[string]any)
		if limit, ok := numericValue(validation["minimum"]); ok && numberValue < limit {
			return false
		}
		if limit, ok := numericValue(validation["maximum"]); ok && numberValue > limit {
			return false
		}
	}
	return true
}

func matchesJSONType(value any, fieldType string) bool {
	switch fieldType {
	case "string", "text", "enum", "date", "datetime":
		text, ok := value.(string)
		if !ok {
			return false
		}
		if fieldType == "date" {
			_, err := time.Parse("2006-01-02", text)
			return err == nil
		}
		if fieldType == "datetime" {
			_, err := time.Parse(time.RFC3339, text)
			return err == nil
		}
		return true
	case "number":
		_, ok := numericValue(value)
		return ok
	case "integer":
		number, ok := numericValue(value)
		return ok && number == float64(int64(number))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array", "multiselect":
		_, ok := value.([]any)
		return ok
	case "asset_reference":
		reference, ok := value.(map[string]any)
		if !ok || len(reference) != 2 {
			return false
		}
		assetID, assetOK := reference["asset_id"].(string)
		versionID, versionOK := reference["asset_version_id"].(string)
		return assetOK && versionOK && validID(assetID) && validID(versionID)
	case "null":
		return value == nil
	default:
		return true
	}
}

func matchesOption(value any, raw any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	options, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, rawOption := range options {
		option, ok := rawOption.(map[string]any)
		if ok && option["value"] == text {
			return true
		}
	}
	return false
}

type assetReferenceValue struct {
	AssetID        string
	AssetVersionID string
}

func validateAssetReferences(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID string, schemaBytes []byte, fields map[string]any) error {
	var schema map[string]any
	if json.Unmarshal(schemaBytes, &schema) != nil {
		return ErrInvalidInput
	}
	definitions, _, _ := schemaProperties(schema)
	references := make([]assetReferenceValue, 0)
	for key, value := range fields {
		definition, _ := definitions[key].(map[string]any)
		collectAssetReferences(value, definition, &references)
	}
	for _, reference := range references {
		var allowed bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM asset.assets a
				JOIN asset.asset_versions av ON av.organization_id = a.organization_id
				  AND av.asset_id = a.id AND av.id = $4::uuid
				WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
				  AND a.id = $3::uuid AND a.deleted_at IS NULL
				  AND (
					($5 = 'member')
					OR ($5 = 'agent' AND EXISTS (
						SELECT 1 FROM content.agent_access_policies ap
						WHERE ap.organization_id = a.organization_id AND ap.workspace_id = a.workspace_id
						  AND ap.agent_user_id = $6::uuid
						  AND (ap.resource_model_id IS NULL OR ap.resource_model_id = a.resource_model_id)
						  AND 'asset.read' = ANY(ap.actions)
					))
				  )
			)
		`, principal.OrganizationID, workspaceID, reference.AssetID, reference.AssetVersionID, principal.UserType, principal.UserID).Scan(&allowed); err != nil {
			return fmt.Errorf("validate asset reference: %w", err)
		}
		if !allowed {
			return ErrInvalidInput
		}
	}
	return nil
}

func collectAssetReferences(value any, definition map[string]any, result *[]assetReferenceValue) {
	fieldType, _ := definition["type"].(string)
	switch fieldType {
	case "asset_reference":
		reference, _ := value.(map[string]any)
		assetID, _ := reference["asset_id"].(string)
		versionID, _ := reference["asset_version_id"].(string)
		*result = append(*result, assetReferenceValue{AssetID: assetID, AssetVersionID: versionID})
	case "object":
		object, _ := value.(map[string]any)
		properties, _ := definition["properties"].(map[string]any)
		for key, item := range object {
			child, _ := properties[key].(map[string]any)
			collectAssetReferences(item, child, result)
		}
	case "array":
		items, _ := definition["items"].(map[string]any)
		values, _ := value.([]any)
		for _, item := range values {
			collectAssetReferences(item, items, result)
		}
	}
}

func numericValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func hashRequest(operation string, value any) string {
	payload, _ := json.Marshal(struct {
		Operation string `json:"operation"`
		Value     any    `json:"value"`
	}{operation, value})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

func validIdempotencyKey(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 16 && len(value) <= 200
}

const (
	httpCreated  = 201
	httpOK       = 200
	httpAccepted = 202
)
