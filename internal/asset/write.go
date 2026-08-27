package asset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/retrieval"

	"github.com/jackc/pgx/v5"
)

var ErrIdempotencyConflict = errors.New("idempotency key conflict")

type AssetResult struct {
	ID                        string         `json:"id"`
	ResourceModelID           string         `json:"resource_model_id"`
	CurrentWorkingVersionID   string         `json:"current_working_version_id"`
	CurrentPublishedVersionID *string        `json:"current_published_version_id"`
	PublicationStatus         string         `json:"publication_status"`
	WorkflowStatus            string         `json:"workflow_status"`
	Quality                   string         `json:"quality"`
	Title                     *string        `json:"title"`
	Markdown                  *string        `json:"markdown,omitempty"`
	Fields                    map[string]any `json:"fields"`
	UpdatedAt                 time.Time      `json:"updated_at"`
}

type CreateInput struct {
	ResourceModelID string
	Title           *string
	Markdown        *string
	Fields          map[string]any
	Source          map[string]any
	// Tags is optional; channel integrations (import/webhook) seed the
	// version-level tag list, plain API creates leave it empty.
	Tags []string
	// VersionSource is written to asset_versions.source when set. It records
	// channel provenance such as webhook received_at or import row numbers.
	VersionSource map[string]any
}

type UpdateInput struct {
	Title    *string
	Markdown *string
	Fields   *map[string]any
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
	sourcePayload, _ := json.Marshal(input)
	var rawInputID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.raw_inputs
			(organization_id, submitted_by, source_type, content_type, payload, content_checksum)
		VALUES ($1::uuid, $2::uuid, 'api', 'application/json', $3::jsonb, $4)
		RETURNING id::text
	`, principal.OrganizationID, principal.UserID, string(sourcePayload), contentChecksum).Scan(&rawInputID); err != nil {
		return AssetResult{}, fmt.Errorf("record raw input: %w", err)
	}
	var modelVersionID, workspaceID string
	var fieldSchema []byte
	if err := tx.QueryRow(ctx, `
		SELECT mv.id::text, mv.field_schema, rm.workspace_id::text
		FROM model.resource_models rm
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		WHERE rm.id = $1::uuid
		  AND rm.organization_id = $2::uuid
		  AND rm.id::text = ANY($3::text[])
		  AND rm.status = 'active'
	`, input.ResourceModelID, principal.OrganizationID, allowedModelIDs).Scan(&modelVersionID, &fieldSchema, &workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AssetResult{}, ErrNotFound
		}
		return AssetResult{}, fmt.Errorf("load resource model version: %w", err)
	}
	if err := validateFields(fieldSchema, fields); err != nil {
		return AssetResult{}, err
	}
	if err := validateAssetReferences(ctx, tx, principal, workspaceID, fieldSchema, fields); err != nil {
		return AssetResult{}, err
	}
	var assetID, versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, input.ResourceModelID, principal.UserID).Scan(&assetID); err != nil {
		return AssetResult{}, fmt.Errorf("create asset: %w", err)
	}
	tagsArg := "[]"
	if len(input.Tags) > 0 {
		tagsArg = string(mustJSON(input.Tags))
	}
	versionSourceArg := "{}"
	if input.VersionSource != nil {
		versionSourceArg = string(mustJSON(input.VersionSource))
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id, version_no,
			 workflow_status, quality, title, markdown, fields, source_raw_input_id, content_checksum, created_by,
			 tags, source)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 1, 'draft', 'raw', $6, $7, $8::jsonb, $9::uuid, $10, $11::uuid, $12::jsonb, $13::jsonb)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, assetID, input.ResourceModelID, modelVersionID, input.Title, input.Markdown, string(mustJSON(fields)), rawInputID, contentChecksum, principal.UserID, tagsArg, versionSourceArg).Scan(&versionID); err != nil {
		return AssetResult{}, fmt.Errorf("create asset version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $2::uuid, updated_at = now()
		WHERE id = $1::uuid
	`, assetID, versionID); err != nil {
		return AssetResult{}, fmt.Errorf("set working asset version: %w", err)
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, versionID, retrieval.ProjectionRebuild); err != nil {
		return AssetResult{}, fmt.Errorf("enqueue asset projection: %w", err)
	}
	result, err := loadAssetTx(ctx, tx, assetID)
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

func (s Service) Update(ctx context.Context, principal auth.Principal, allowedModelIDs []string, idempotencyKey, assetID, expectedVersionID string, input UpdateInput) (AssetResult, error) {
	if len(allowedModelIDs) == 0 || !validID(assetID) || !validID(expectedVersionID) || !validIdempotencyKey(idempotencyKey) {
		return AssetResult{}, ErrInvalidInput
	}
	if input.Title == nil && input.Markdown == nil && input.Fields == nil {
		return AssetResult{}, ErrInvalidInput
	}
	if err := validateContent(input.Title, input.Markdown, input.Fields); err != nil {
		return AssetResult{}, err
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AssetResult{}, errors.New("database store is not initialized")
	}
	requestHash := hashRequest("asset.update", struct {
		AssetID string
		Version string
		Input   UpdateInput
	}{assetID, expectedVersionID, input})
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
	var currentVersion, workspaceID, modelID, modelVersionID string
	var fieldSchema []byte
	var versionNo int
	var workflow string
	var title, markdown *string
	var fields map[string]any
	err = tx.QueryRow(ctx, `
		SELECT v.id::text, a.workspace_id::text, v.resource_model_id::text, v.resource_model_version_id::text,
		       v.version_no, v.workflow_status, v.title, v.markdown, v.fields
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.id = a.current_working_version_id
		JOIN model.resource_model_versions mv ON mv.id = v.resource_model_version_id
		WHERE a.id = $1::uuid
		  AND a.organization_id = $2::uuid
		  AND a.resource_model_id::text = ANY($3::text[])
		FOR UPDATE OF a, v
	`, assetID, principal.OrganizationID, allowedModelIDs).Scan(&currentVersion, &workspaceID, &modelID, &modelVersionID, &versionNo, &workflow, &title, &markdown, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssetResult{}, ErrNotFound
	}
	if err != nil {
		return AssetResult{}, fmt.Errorf("load asset for update: %w", err)
	}
	if currentVersion != expectedVersionID {
		return AssetResult{}, fmt.Errorf("%w: working version changed", ErrConflict)
	}
	if workflow != "draft" {
		return AssetResult{}, fmt.Errorf("%w: version is not editable", ErrConflict)
	}
	if input.Title != nil {
		title = input.Title
	}
	if input.Markdown != nil {
		markdown = input.Markdown
	}
	if input.Fields != nil {
		fields = *input.Fields
	}
	if fields == nil {
		fields = map[string]any{}
	}
	if err := tx.QueryRow(ctx, `SELECT field_schema FROM model.resource_model_versions WHERE id = $1::uuid`, modelVersionID).Scan(&fieldSchema); err != nil {
		return AssetResult{}, fmt.Errorf("load resource model field schema: %w", err)
	}
	if err := validateFields(fieldSchema, fields); err != nil {
		return AssetResult{}, err
	}
	if err := validateAssetReferences(ctx, tx, principal, workspaceID, fieldSchema, fields); err != nil {
		return AssetResult{}, err
	}
	contentChecksum := hashRequest("asset.content", struct {
		Title    *string        `json:"title"`
		Markdown *string        `json:"markdown"`
		Fields   map[string]any `json:"fields"`
	}{title, markdown, fields})
	var newVersionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id, version_no,
			 workflow_status, quality, title, markdown, fields, parent_version_id, content_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'draft', 'raw', $7, $8, $9::jsonb, $10::uuid, $11, $12::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, assetID, modelID, modelVersionID, versionNo+1, title, markdown, string(mustJSON(fields)), currentVersion, contentChecksum, principal.UserID).Scan(&newVersionID); err != nil {
		return AssetResult{}, fmt.Errorf("create updated asset version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $2::uuid, updated_at = now()
		WHERE id = $1::uuid
	`, assetID, newVersionID); err != nil {
		return AssetResult{}, fmt.Errorf("set updated working version: %w", err)
	}
	if err := retrieval.EnqueueProjectionTx(ctx, tx, s.Events, principal.OrganizationID, newVersionID, retrieval.ProjectionRebuild); err != nil {
		return AssetResult{}, fmt.Errorf("enqueue updated asset projection: %w", err)
	}
	result, err := loadAssetTx(ctx, tx, assetID)
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

func loadAssetTx(ctx context.Context, tx pgx.Tx, assetID string) (AssetResult, error) {
	var result AssetResult
	err := tx.QueryRow(ctx, `
		SELECT a.id::text, a.resource_model_id::text,
		       a.current_working_version_id::text, a.current_published_version_id,
		       a.publication_status, v.workflow_status, v.quality, v.title, v.markdown,
		       v.fields, a.updated_at
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.id = a.current_working_version_id
		WHERE a.id = $1::uuid
	`, assetID).Scan(&result.ID, &result.ResourceModelID, &result.CurrentWorkingVersionID, &result.CurrentPublishedVersionID, &result.PublicationStatus, &result.WorkflowStatus, &result.Quality, &result.Title, &result.Markdown, &result.Fields, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssetResult{}, ErrNotFound
	}
	if err != nil {
		return AssetResult{}, fmt.Errorf("load asset result: %w", err)
	}
	return result, nil
}

// ValidateFields exposes the model contract to Agent candidate processors.
func ValidateFields(schemaBytes []byte, fields map[string]any) error {
	return validateFields(schemaBytes, fields)
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
				JOIN asset.asset_versions av ON av.organization_id = a.organization_id AND av.asset_id = a.id
				WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
				  AND a.id = $3::uuid AND av.id = $4::uuid AND a.deleted_at IS NULL
				  AND (
					($5 = 'member' AND (
						a.visibility <> 'private' OR a.created_by = $6::uuid OR EXISTS (
							SELECT 1 FROM content.workspace_members wm
							WHERE wm.organization_id = a.organization_id AND wm.workspace_id = a.workspace_id
							  AND wm.user_id = $6::uuid AND wm.role IN ('owner', 'admin')
						)
					))
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
