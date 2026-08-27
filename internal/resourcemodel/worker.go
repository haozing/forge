package resourcemodel

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var ErrNoPendingMigration = errors.New("no pending resource model migration")

// MigrationProcessor applies one queued resource-model migration in a single
// transaction. Asset versions remain immutable; every migrated asset receives
// a new working version pointing at the target model version.
type MigrationProcessor struct {
	Store  *store.Store
	Events eventing.EventStore
}

func (p MigrationProcessor) ProcessNext(ctx context.Context) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var migrationID string
	err := p.Store.Pool.QueryRow(ctx, `
		WITH next_job AS (
			SELECT id
			FROM model.resource_model_migrations
			WHERE status = 'queued'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE model.resource_model_migrations m
		SET status = 'processing', error_summary = NULL, completed_at = NULL
		FROM next_job
		WHERE m.id = next_job.id
		RETURNING m.id::text
	`).Scan(&migrationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoPendingMigration
	}
	if err != nil {
		return fmt.Errorf("claim resource model migration: %w", err)
	}
	if err := p.process(ctx, migrationID); err != nil {
		p.fail(ctx, migrationID, err)
		return err
	}
	return nil
}

func (p MigrationProcessor) process(ctx context.Context, migrationID string) error {
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin resource model migration: %w", err)
	}
	defer tx.Rollback(ctx)
	var organizationID, workspaceID, modelID, targetVersionID string
	var sourceVersionID *string
	var snapshotRaw, targetSchema []byte
	if err := tx.QueryRow(ctx, `
		SELECT m.organization_id::text, m.workspace_id::text, m.resource_model_id::text,
		       m.from_version_id::text, m.to_version_id::text, m.input_snapshot,
		       v.field_schema
		FROM model.resource_model_migrations m
		JOIN model.resource_model_versions v ON v.id = m.to_version_id
		WHERE m.id = $1::uuid AND m.status = 'processing'
		FOR UPDATE OF m
	`, migrationID).Scan(&organizationID, &workspaceID, &modelID, &sourceVersionID, &targetVersionID, &snapshotRaw, &targetSchema); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoPendingMigration
		}
		return fmt.Errorf("load resource model migration: %w", err)
	}
	var snapshot map[string]any
	if len(snapshotRaw) > 0 && json.Unmarshal(snapshotRaw, &snapshot) != nil {
		return errors.New("resource model migration snapshot is invalid")
	}
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	var sourceArg any
	if sourceVersionID != nil {
		sourceArg = *sourceVersionID
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, v.id::text, v.version_no, v.title, v.markdown, v.fields,
		       v.quality, v.tags, v.source, v.created_by::text
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.id = a.current_working_version_id
		WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
		  AND a.resource_model_id = $3::uuid AND a.publication_status <> 'archived'
		  AND ($4::uuid IS NULL OR v.resource_model_version_id = $4::uuid)
		FOR UPDATE OF a, v
	`, organizationID, workspaceID, modelID, sourceArg)
	if err != nil {
		return fmt.Errorf("list migration assets: %w", err)
	}
	defer rows.Close()
	// Buffer candidates before writing: a pgx Tx is bound to one connection, so
	// issuing INSERTs while this result set is still open fails with conn busy.
	type migrationAsset struct {
		assetID, oldVersionID, createdBy string
		versionNo                        int
		title, markdown                  *string
		fields, tags, source             []byte
		quality                          string
	}
	pending := []migrationAsset{}
	for rows.Next() {
		var item migrationAsset
		if err := rows.Scan(&item.assetID, &item.oldVersionID, &item.versionNo, &item.title, &item.markdown, &item.fields, &item.quality, &item.tags, &item.source, &item.createdBy); err != nil {
			return fmt.Errorf("scan migration asset: %w", err)
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migration assets: %w", err)
	}
	rows.Close()
	for _, item := range pending {
		assetID, oldVersionID, createdBy := item.assetID, item.oldVersionID, item.createdBy
		versionNo := item.versionNo
		var fieldMap map[string]any
		if len(item.fields) > 0 && string(item.fields) != "null" {
			if err := json.Unmarshal(item.fields, &fieldMap); err != nil {
				return fmt.Errorf("decode migration fields: %w", err)
			}
		}
		migrated := transformFields(fieldMap, snapshot, targetSchema)
		if err := validateMigrationFields(targetSchema, migrated); err != nil {
			return fmt.Errorf("asset %s cannot migrate: %w", assetID, err)
		}
		checksum := checksumForMigration(item.title, item.markdown, migrated)
		var newVersionID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO asset.asset_versions
				(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
				 version_no, workflow_status, quality, title, markdown, fields, tags, source,
				 parent_version_id, content_checksum, created_by, review_status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6,
				'draft', $7, $8, $9, $10::jsonb, $11::jsonb, $12::jsonb,
				$13::uuid, $14, $15::uuid, 'none')
			RETURNING id::text
		`, organizationID, workspaceID, assetID, modelID, targetVersionID, versionNo+1,
			item.quality, item.title, item.markdown, mustJSON(migrated), jsonOrEmpty(item.tags), jsonOrEmpty(item.source), oldVersionID, checksum, createdBy).Scan(&newVersionID); err != nil {
			return fmt.Errorf("create migrated asset version: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_working_version_id = $2::uuid, updated_at = now() WHERE id = $1::uuid`, assetID, newVersionID); err != nil {
			return fmt.Errorf("set migrated working version: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE asset.asset_reviews SET status = 'superseded', reviewed_at = now() WHERE asset_version_id = $1::uuid AND status = 'pending'`, oldVersionID); err != nil {
			return fmt.Errorf("supersede migration review: %w", err)
		}
		if err := retrieval.EnqueueProjectionTx(ctx, tx, p.Events, organizationID, newVersionID, retrieval.ProjectionRebuild); err != nil {
			return fmt.Errorf("enqueue migrated projection: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migration assets: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE model.resource_model_migrations SET status = 'succeeded', completed_at = now(), error_summary = NULL WHERE id = $1::uuid`, migrationID); err != nil {
		return fmt.Errorf("complete resource model migration: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit resource model migration: %w", err)
	}
	return nil
}

func validateMigrationFields(rawSchema []byte, fields map[string]any) error {
	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return ErrSchemaInvalid
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
		if definitions, ok := schema["fields"].([]any); ok {
			for _, raw := range definitions {
				definition, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				key, _ := definition["key"].(string)
				if key == "" {
					key, _ = definition["name"].(string)
				}
				if key != "" {
					properties[key] = definition
				}
			}
		}
	}
	additional, _ := schema["additionalProperties"].(bool)
	if !additional {
		for key := range fields {
			if _, ok := properties[key]; !ok {
				return ErrSchemaInvalid
			}
		}
	}
	for _, raw := range schemaRequired(schema, properties) {
		if value, ok := fields[raw]; !ok || value == nil {
			return ErrSchemaInvalid
		}
	}
	for key, value := range fields {
		definition, _ := properties[key].(map[string]any)
		if definition == nil {
			continue
		}
		if fieldType, _ := definition["type"].(string); fieldType != "" && !migrationTypeMatches(value, fieldType) {
			return ErrSchemaInvalid
		}
	}
	return nil
}

func schemaRequired(schema map[string]any, properties map[string]any) []string {
	result := []string{}
	if values, ok := schema["required"].([]any); ok {
		for _, value := range values {
			if key, ok := value.(string); ok {
				result = append(result, key)
			}
		}
	}
	for key, raw := range properties {
		if definition, ok := raw.(map[string]any); ok {
			if required, _ := definition["required"].(bool); required {
				result = append(result, key)
			}
		}
	}
	return result
}

func migrationTypeMatches(value any, fieldType string) bool {
	switch fieldType {
	case "string", "text", "date", "datetime", "enum":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array", "multiselect":
		_, ok := value.([]any)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case "number":
		_, ok := value.(float64)
		return ok
	default:
		return true
	}
}

func (p MigrationProcessor) fail(ctx context.Context, migrationID string, cause error) {
	_, _ = p.Store.Pool.Exec(ctx, `UPDATE model.resource_model_migrations SET status = 'failed', error_summary = $2, completed_at = now() WHERE id = $1::uuid AND status = 'processing'`, migrationID, truncateError(cause))
}

func transformFields(source map[string]any, snapshot map[string]any, targetSchema []byte) map[string]any {
	result := map[string]any{}
	var schema map[string]any
	_ = json.Unmarshal(targetSchema, &schema)
	mapping, _ := snapshot["mapping"].(map[string]any)
	defaults, _ := snapshot["defaults"].(map[string]any)
	for _, key := range schemaKeys(schema) {
		if sourceValue, ok := source[key]; ok {
			result[key] = sourceValue
			continue
		}
		if mapped, ok := mapping[key].(string); ok {
			if sourceValue, exists := source[mapped]; exists {
				result[key] = sourceValue
				continue
			}
		}
		if defaultValue, ok := defaults[key]; ok {
			result[key] = defaultValue
		}
	}
	return result
}

func schemaKeys(schema map[string]any) []string {
	keys := []string{}
	if fields, ok := schema["fields"].([]any); ok {
		for _, raw := range fields {
			field, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			key, _ := field["key"].(string)
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func checksumForMigration(title, markdown *string, fields map[string]any) string {
	body, _ := json.Marshal(map[string]any{"title": title, "markdown": markdown, "fields": fields})
	return fmt.Sprintf("%x", sha256Bytes(body))
}

func sha256Bytes(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func jsonOrEmpty(raw []byte) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return []byte("{}")
	}
	return raw
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 2000 {
		return message[:2000]
	}
	return message
}
