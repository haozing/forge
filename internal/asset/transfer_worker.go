package asset

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNoPendingImport = errors.New("no pending import job")
	ErrNoPendingExport = errors.New("no pending export job")
)

type TransferProcessor struct {
	Store        *store.Store
	Events       eventing.EventStore
	Objects      objectstore.ObjectStore
	ObjectPrefix string
}

func (p TransferProcessor) ProcessNextImport(ctx context.Context) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var id string
	err := p.Store.Pool.QueryRow(ctx, `
		WITH next_job AS (
			SELECT id FROM asset.import_batches
			WHERE status = 'queued' ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE asset.import_batches b SET status = 'processing', completed_at = NULL
		FROM next_job WHERE b.id = next_job.id RETURNING b.id::text
	`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoPendingImport
	}
	if err != nil {
		return fmt.Errorf("claim import job: %w", err)
	}
	if err := p.processImport(ctx, id); err != nil {
		if failErr := p.failImport(ctx, id, err); failErr != nil {
			return fmt.Errorf("process import: %v; mark import failed: %w", err, failErr)
		}
		return err
	}
	return nil
}

func (p TransferProcessor) ProcessImportJob(ctx context.Context, id string) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var claimed string
	if err := p.Store.Pool.QueryRow(ctx, `UPDATE asset.import_batches SET status = 'processing', completed_at = NULL WHERE id = $1::uuid AND status = 'queued' RETURNING id::text`, id).Scan(&claimed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoPendingImport
		}
		return err
	}
	if err := p.processImport(ctx, claimed); err != nil {
		if failErr := p.failImport(ctx, claimed, err); failErr != nil {
			return fmt.Errorf("process import: %v; mark import failed: %w", err, failErr)
		}
		return err
	}
	return nil
}

func (p TransferProcessor) processImport(ctx context.Context, batchID string) error {
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var organizationID, workspaceID, modelID, versionID, sourceName, submittedBy string
	var fieldSchema []byte
	if err := tx.QueryRow(ctx, `
		SELECT b.organization_id::text, rm.workspace_id::text, b.resource_model_id::text,
		       b.resource_model_version_id::text, COALESCE(b.source_name, ''), b.submitted_by::text, v.field_schema
		FROM asset.import_batches b
		JOIN model.resource_models rm ON rm.id = b.resource_model_id
		JOIN model.resource_model_versions v ON v.id = b.resource_model_version_id
		WHERE b.id = $1::uuid AND b.status = 'processing'
		FOR UPDATE OF b
	`, batchID).Scan(&organizationID, &workspaceID, &modelID, &versionID, &sourceName, &submittedBy, &fieldSchema); err != nil {
		return fmt.Errorf("load import job: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text, row_number, source_row FROM asset.import_rows WHERE import_batch_id = $1::uuid AND status = 'pending' ORDER BY row_number FOR UPDATE`, batchID)
	if err != nil {
		return fmt.Errorf("load import rows: %w", err)
	}
	type pendingImportRow struct {
		id        string
		number    int
		sourceRow []byte
	}
	pendingRows := make([]pendingImportRow, 0)
	for rows.Next() {
		var row pendingImportRow
		if err := rows.Scan(&row.id, &row.number, &row.sourceRow); err != nil {
			rows.Close()
			return fmt.Errorf("scan import row: %w", err)
		}
		pendingRows = append(pendingRows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	accepted, rejected := 0, 0
	for _, row := range pendingRows {
		rowID, rowNumber, raw := row.id, row.number, row.sourceRow
		var sourceRow map[string]any
		if err := json.Unmarshal(raw, &sourceRow); err != nil {
			if err := rejectImportRow(ctx, tx, rowID, "invalid_json"); err != nil {
				return err
			}
			rejected++
			continue
		}
		title := stringPointer(sourceRow["title"])
		markdown := stringPointer(sourceRow["markdown"])
		fields := importFields(sourceRow)
		if err := ValidateContent(title, markdown, &fields); err != nil {
			if err := rejectImportRow(ctx, tx, rowID, "invalid_content"); err != nil {
				return err
			}
			rejected++
			continue
		}
		if err := ValidateFields(fieldSchema, fields); err != nil {
			if err := rejectImportRow(ctx, tx, rowID, "invalid_fields"); err != nil {
				return err
			}
			rejected++
			continue
		}
		payload, _ := json.Marshal(sourceRow)
		sum := sha256.Sum256(payload)
		var rawInputID, assetID, assetVersionID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO asset.raw_inputs (organization_id, submitted_by, source_type, content_type, payload, content_checksum)
			VALUES ($1::uuid, $2::uuid, 'import', 'application/json', $3::jsonb, $4)
			RETURNING id::text
		`, organizationID, submittedBy, string(payload), hex.EncodeToString(sum[:])).Scan(&rawInputID); err != nil {
			return fmt.Errorf("create import raw input: %w", err)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by) SELECT b.organization_id, rm.workspace_id, b.resource_model_id, b.submitted_by FROM asset.import_batches b JOIN model.resource_models rm ON rm.id = b.resource_model_id WHERE b.id = $1::uuid RETURNING id::text`, batchID).Scan(&assetID); err != nil {
			return fmt.Errorf("create imported asset: %w", err)
		}
		checksum := hex.EncodeToString(sum[:])
		if err := tx.QueryRow(ctx, `
			INSERT INTO asset.asset_versions
				(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
				 version_no, workflow_status, quality, title, markdown, fields, source_raw_input_id,
				 content_checksum, created_by, source)
			SELECT b.organization_id, rm.workspace_id, $2::uuid, b.resource_model_id, b.resource_model_version_id,
				1, 'draft', 'raw', $3, $4, $5::jsonb, $6::uuid, $7, b.submitted_by, $8::jsonb
			FROM asset.import_batches b JOIN model.resource_models rm ON rm.id = b.resource_model_id
			WHERE b.id = $1::uuid RETURNING id::text
		`, batchID, assetID, title, markdown, mustJSON(fields), rawInputID, checksum, mustJSON(map[string]any{"source_name": sourceName, "row_number": rowNumber})).Scan(&assetVersionID); err != nil {
			return fmt.Errorf("create imported asset version: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE asset.import_rows SET raw_input_id = $2::uuid WHERE id = $1::uuid`, rowID, rawInputID); err != nil {
			return fmt.Errorf("link imported raw input: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE asset.assets SET current_working_version_id = $2::uuid, updated_at = now() WHERE id = $1::uuid`, assetID, assetVersionID); err != nil {
			return fmt.Errorf("set imported working version: %w", err)
		}
		if err := retrieval.EnqueueProjectionTx(ctx, tx, p.Events, organizationID, assetVersionID, retrieval.ProjectionRebuild); err != nil {
			return fmt.Errorf("enqueue imported projection: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE asset.import_rows SET status = 'accepted', errors = '[]'::jsonb WHERE id = $1::uuid`, rowID); err != nil {
			return err
		}
		accepted++
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.import_batches SET status = 'succeeded', summary = $2::jsonb, completed_at = now() WHERE id = $1::uuid`, batchID, mustJSON(map[string]any{"rows_total": accepted + rejected, "rows_accepted": accepted, "rows_rejected": rejected})); err != nil {
		return fmt.Errorf("complete import job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit import job: %w", err)
	}
	return nil
}

func (p TransferProcessor) ProcessNextExport(ctx context.Context) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var id string
	err := p.Store.Pool.QueryRow(ctx, `
		WITH next_job AS (SELECT id FROM asset.export_jobs WHERE status = 'queued' ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE asset.export_jobs e SET status = 'processing', completed_at = NULL
		FROM next_job WHERE e.id = next_job.id RETURNING e.id::text
	`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoPendingExport
	}
	if err != nil {
		return fmt.Errorf("claim export job: %w", err)
	}
	if err := p.processExport(ctx, id); err != nil {
		if failErr := p.failExport(ctx, id, err); failErr != nil {
			return fmt.Errorf("process export: %v; mark export failed: %w", err, failErr)
		}
		return err
	}
	return nil
}

func (p TransferProcessor) ProcessExportJob(ctx context.Context, id string) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var claimed string
	if err := p.Store.Pool.QueryRow(ctx, `UPDATE asset.export_jobs SET status = 'processing', completed_at = NULL WHERE id = $1::uuid AND status = 'queued' RETURNING id::text`, id).Scan(&claimed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoPendingExport
		}
		return err
	}
	if err := p.processExport(ctx, claimed); err != nil {
		if failErr := p.failExport(ctx, claimed, err); failErr != nil {
			return fmt.Errorf("process export: %v; mark export failed: %w", err, failErr)
		}
		return err
	}
	return nil
}

func (p TransferProcessor) processExport(ctx context.Context, jobID string) error {
	if p.Objects == nil {
		return errors.New("object store is not configured")
	}
	var organizationID, modelID string
	var snapshotRaw, permissionRaw []byte
	if err := p.Store.Pool.QueryRow(ctx, `SELECT ej.organization_id::text, ej.resource_model_id::text, ej.query_snapshot, ej.permission_scope FROM asset.export_jobs ej WHERE ej.id = $1::uuid AND ej.status = 'processing'`, jobID).Scan(&organizationID, &modelID, &snapshotRaw, &permissionRaw); err != nil {
		return err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil {
		return fmt.Errorf("decode export snapshot: %w", err)
	}
	var permissionScope map[string]any
	if err := json.Unmarshal(permissionRaw, &permissionScope); err != nil {
		return fmt.Errorf("decode export permission scope: %w", err)
	}
	workspaceID, _ := permissionScope["workspace_id"].(string)
	if !validID(workspaceID) {
		return errors.New("export permission scope has no workspace")
	}
	format, _ := snapshot["format"].(string)
	if format == "" {
		format = "jsonl"
	}
	query, args := exportAssetQuery(organizationID, workspaceID, modelID, snapshot)
	rows, err := p.Store.Pool.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	headers := []string{"id", "title", "markdown", "fields", "tags", "visibility", "publication_status", "review_status"}
	records := make([][]string, 0, 128)
	for rows.Next() {
		var id, visibility, publication, review string
		var title, markdown *string
		var fields, tags []byte
		if err := rows.Scan(&id, &title, &markdown, &fields, &tags, &visibility, &publication, &review); err != nil {
			return err
		}
		records = append(records, []string{id, derefString(title), derefString(markdown), string(fields), string(tags), visibility, publication, review})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var body bytes.Buffer
	var contentType string
	switch format {
	case "csv":
		writer := csv.NewWriter(&body)
		if err := writer.Write(headers); err != nil {
			return err
		}
		for _, record := range records {
			if err := writer.Write(record); err != nil {
				return err
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		contentType = "text/csv"
	case "xlsx":
		records = append([][]string{headers}, records...)
		xlsx, err := writeSimpleXLSX(records)
		if err != nil {
			return err
		}
		body.Write(xlsx)
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		for _, record := range records {
			line, _ := json.Marshal(map[string]any{
				"id": record[0], "title": nullableString(record[1]), "markdown": nullableString(record[2]),
				"fields": decodeJSONMap([]byte(record[3])), "tags": decodeStringSlice([]byte(record[4])),
				"visibility": record[5], "publication_status": record[6], "review_status": record[7],
			})
			body.Write(line)
			body.WriteByte('\n')
		}
		contentType = "application/x-ndjson"
	}
	sum := sha256.Sum256(body.Bytes())
	prefix := strings.Trim(strings.TrimSpace(p.ObjectPrefix), "/")
	key := fmt.Sprintf("exports/%s/%s.%s", organizationID, jobID, format)
	if prefix != "" {
		key = prefix + "/" + key
	}
	if _, err := p.Objects.Put(ctx, objectstore.Object{Key: key, Body: bytes.NewReader(body.Bytes()), ContentType: contentType, ContentLength: int64(body.Len())}); err != nil {
		return err
	}
	if _, err := p.Store.Pool.Exec(ctx, `UPDATE asset.export_jobs SET status = 'succeeded', output_object_key = $2, output_content_type = $3, output_size = $4, output_checksum = $5, completed_at = now(), error_code = NULL WHERE id = $1::uuid`, jobID, key, contentType, body.Len(), hex.EncodeToString(sum[:])); err != nil {
		return err
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// writeSimpleXLSX creates a standards-compliant workbook with one worksheet.
// Inline strings keep this exporter dependency-free and preserve JSON fields as text.
func writeSimpleXLSX(records [][]string) ([]byte, error) {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	files := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Assets" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
	}
	for name, data := range files {
		entry, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(data)); err != nil {
			return nil, err
		}
	}
	sheet, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, err
	}
	if _, err := sheet.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)); err != nil {
		return nil, err
	}
	for rowIndex, record := range records {
		row := fmt.Sprintf(`<row r="%d">`, rowIndex+1)
		if _, err := sheet.Write([]byte(row)); err != nil {
			return nil, err
		}
		for columnIndex, value := range record {
			cellRef := fmt.Sprintf("%s%d", xlsxColumnName(columnIndex+1), rowIndex+1)
			cell := fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, cellRef, html.EscapeString(value))
			if _, err := sheet.Write([]byte(cell)); err != nil {
				return nil, err
			}
		}
		if _, err := sheet.Write([]byte(`</row>`)); err != nil {
			return nil, err
		}
	}
	if _, err := sheet.Write([]byte(`</sheetData></worksheet>`)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func xlsxColumnName(index int) string {
	name := ""
	for index > 0 {
		index--
		name = string(rune('A'+index%26)) + name
		index /= 26
	}
	return name
}

func exportAssetQuery(organizationID, workspaceID, modelID string, snapshot map[string]any) (string, []any) {
	args := []any{organizationID, workspaceID, modelID}
	where := []string{
		"a.organization_id = $1::uuid",
		"a.workspace_id = $2::uuid",
		"a.resource_model_id = $3::uuid",
		"a.publication_status <> 'archived'",
	}
	filters, _ := snapshot["filters"].(map[string]any)
	add := func(clause string, value any) string {
		args = append(args, value)
		return fmt.Sprintf(clause, len(args))
	}
	if query, ok := filters["q"].(string); ok && strings.TrimSpace(query) != "" {
		args = append(args, query)
		placeholder := len(args)
		where = append(where, fmt.Sprintf("(COALESCE(v.title, '') ILIKE '%%' || $%d || '%%' OR COALESCE(v.markdown, '') ILIKE '%%' || $%d || '%%')", placeholder, placeholder))
	}
	for _, key := range []string{"visibility", "publication_status", "review_status"} {
		if value, ok := filters[key].(string); ok && strings.TrimSpace(value) != "" {
			column := map[string]string{"visibility": "a.visibility", "publication_status": "a.publication_status", "review_status": "v.review_status"}[key]
			where = append(where, add(column+" = $%d", value))
		}
	}
	if tags, ok := filters["tags"].([]any); ok && len(tags) > 0 {
		encoded, _ := json.Marshal(tags)
		where = append(where, add("v.tags @> $%d::jsonb", string(encoded)))
	}
	if fields, ok := filters["fields"].(map[string]any); ok && len(fields) > 0 {
		encoded, _ := json.Marshal(fields)
		where = append(where, add("v.fields @> $%d::jsonb", string(encoded)))
	}
	query := "SELECT a.id::text, v.title, v.markdown, v.fields, v.tags, a.visibility, a.publication_status, v.review_status FROM asset.assets a JOIN asset.asset_versions v ON v.id = COALESCE(a.current_published_version_id, a.current_working_version_id) WHERE " + strings.Join(where, " AND ") + " ORDER BY a.id"
	return query, args
}

func (p TransferProcessor) failImport(ctx context.Context, id string, cause error) error {
	_, err := p.Store.Pool.Exec(ctx, `UPDATE asset.import_batches SET status = 'failed', summary = jsonb_build_object('error', $2::text), completed_at = now() WHERE id = $1::uuid AND status = 'processing'`, id, truncateTransferError(cause))
	return err
}
func (p TransferProcessor) failExport(ctx context.Context, id string, cause error) error {
	_, err := p.Store.Pool.Exec(ctx, `UPDATE asset.export_jobs SET status = 'failed', error_code = $2, completed_at = now() WHERE id = $1::uuid AND status = 'processing'`, id, truncateTransferError(cause))
	return err
}

func rejectImportRow(ctx context.Context, tx pgx.Tx, id, code string) error {
	_, err := tx.Exec(ctx, `UPDATE asset.import_rows SET status = 'rejected', errors = jsonb_build_array(jsonb_build_object('code', $2)) WHERE id = $1::uuid`, id, code)
	return err
}
func importFields(row map[string]any) map[string]any {
	result := map[string]any{}
	if fields, ok := row["fields"].(map[string]any); ok {
		for key, value := range fields {
			result[key] = value
		}
		return result
	}
	for key, value := range row {
		if key != "title" && key != "markdown" && key != "source" {
			result[key] = value
		}
	}
	return result
}
func stringPointer(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func truncateTransferError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}
