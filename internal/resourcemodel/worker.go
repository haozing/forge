package resourcemodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/eventing"
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
	var organizationID, workspaceID, modelID, targetVersionID, migrationCreatedBy string
	var sourceVersionID *string
	var snapshotRaw, targetSchema []byte
	if err := tx.QueryRow(ctx, `
		SELECT m.organization_id::text, m.workspace_id::text, m.resource_model_id::text,
		       m.from_version_id::text, m.to_version_id::text, m.input_snapshot,
		       m.created_by::text, v.field_schema
		FROM model.resource_model_migrations m
		JOIN model.resource_model_versions v ON v.organization_id = m.organization_id AND v.id = m.to_version_id
		WHERE m.id = $1::uuid AND m.status = 'processing'
		FOR UPDATE OF m
	`, migrationID).Scan(&organizationID, &workspaceID, &modelID, &sourceVersionID, &targetVersionID, &snapshotRaw, &migrationCreatedBy, &targetSchema); err != nil {
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
		SELECT a.id::text, v.id::text, COALESCE(v.title, ''), COALESCE(v.summary, ''), COALESCE(v.markdown, ''),
		       v.fields, v.origin, v.created_by::text
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.organization_id = a.organization_id AND v.id = a.current_working_version_id
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
		assetID, oldVersionID, origin, createdBy string
		title, summary, markdown                 string
		fields                                   []byte
	}
	pending := []migrationAsset{}
	for rows.Next() {
		var item migrationAsset
		if err := rows.Scan(&item.assetID, &item.oldVersionID, &item.title, &item.summary, &item.markdown, &item.fields, &item.origin, &item.createdBy); err != nil {
			return fmt.Errorf("scan migration asset: %w", err)
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migration assets: %w", err)
	}
	rows.Close()
	for _, item := range pending {
		assetID, oldVersionID := item.assetID, item.oldVersionID
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
		// Inherit workspace-scoped tag and attachment identities from the old
		// working version through the relation tables.
		var tagIDs, attachmentIDs []string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(array_agg(tag_id::text ORDER BY tag_id), '{}')
			FROM asset.asset_version_tags
			WHERE organization_id = $1::uuid AND asset_version_id = $2::uuid
		`, organizationID, oldVersionID).Scan(&tagIDs); err != nil {
			return fmt.Errorf("load migration version tags: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(array_agg(attachment_id::text ORDER BY attachment_id), '{}')
			FROM asset.asset_version_attachments
			WHERE organization_id = $1::uuid AND asset_version_id = $2::uuid
		`, organizationID, oldVersionID).Scan(&attachmentIDs); err != nil {
			return fmt.Errorf("load migration version attachments: %w", err)
		}
		createdBy := item.createdBy
		if migrationCreatedBy != "" {
			createdBy = migrationCreatedBy
		}
		_, _, err := asset.CreateVersionTx(ctx, tx, asset.VersionMaterial{
			OrganizationID:         organizationID,
			WorkspaceID:            workspaceID,
			AssetID:                assetID,
			ResourceModelID:        modelID,
			ResourceModelVersionID: targetVersionID,
			ParentVersionID:        oldVersionID,
			Origin:                 item.origin,
			ConfirmationStatus:     asset.ConfirmationUnconfirmed,
			Title:                  item.title,
			Summary:                item.summary,
			Markdown:               item.markdown,
			Fields:                 migrated,
			TagIDs:                 tagIDs,
			AttachmentIDs:          attachmentIDs,
			CreatedBy:              createdBy,
		})
		if err != nil {
			return fmt.Errorf("create migrated asset version: %w", err)
		}
		// The working pointer moved, so pending publication requests for this
		// asset no longer reference the working version.
		if _, err := tx.Exec(ctx, `
			UPDATE asset.publication_requests
			SET status = 'cancelled', cancel_reason = 'new_version', revision = revision + 1, decided_at = now()
			WHERE asset_id = $1::uuid AND status = 'pending'
		`, assetID); err != nil {
			return fmt.Errorf("cancel migration publication requests: %w", err)
		}
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
