package asset

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNoPendingImport = errors.New("no pending import job")
	ErrNoPendingExport = errors.New("no pending export job")
)

const transferLeaseInterval = "10 minutes"

type TransferProcessor struct {
	Store        *store.Store
	Events       eventing.EventStore
	Objects      objectstore.ObjectStore
	ObjectPrefix string
}

// newLeaseOwner renders a per-run lease token so a crashed run's leases can be
// told apart from live ones (lease_until expiry is what actually frees work).
func newLeaseOwner() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("transfer-%d", os.Getpid())
	}
	return hex.EncodeToString(buf)
}

func (p TransferProcessor) ProcessNextImport(ctx context.Context) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	var id string
	err := p.Store.Pool.QueryRow(ctx, `
		WITH next_job AS (
			SELECT id FROM asset.import_batches
			WHERE status = 'queued'
			   OR (status = 'processing' AND (lease_until IS NULL OR lease_until < now()))
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE asset.import_batches b
		SET status = 'processing', completed_at = NULL,
		    lease_owner = $1, lease_until = now() + interval '`+transferLeaseInterval+`',
		    attempt_count = b.attempt_count + 1
		FROM next_job WHERE b.id = next_job.id RETURNING b.id::text
	`, newLeaseOwner()).Scan(&id)
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
	if err := p.Store.Pool.QueryRow(ctx, `
		UPDATE asset.import_batches
		SET status = 'processing', completed_at = NULL,
		    lease_owner = $2, lease_until = now() + interval '`+transferLeaseInterval+`',
		    attempt_count = attempt_count + 1
		WHERE id = $1::uuid
		  AND (status = 'queued' OR (status = 'processing' AND (lease_until IS NULL OR lease_until < now())))
		RETURNING id::text
	`, id, newLeaseOwner()).Scan(&claimed); err != nil {
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

// importBatchHeader is the immutable context every row of one batch shares.
type importBatchHeader struct {
	id               string
	organizationID   string
	workspaceID      string
	modelID          string
	versionID        string
	sourceName       string
	submittedBy      string
	fieldSchema      []byte
	unknownTagPolicy string
}

type pendingImportRow struct {
	id        string
	number    int
	sourceRow []byte
}

// processImport walks the batch row by row; every row lives in its own short
// transaction so one bad row never rolls back the assets of its siblings. The
// batch summary is recomputed from row statuses in a separate transaction at
// the end, never from in-memory counters.
func (p TransferProcessor) processImport(ctx context.Context, batchID string) error {
	var header importBatchHeader
	if err := p.Store.Pool.QueryRow(ctx, `
		SELECT b.id::text, b.organization_id::text, b.workspace_id::text, b.resource_model_id::text,
		       b.resource_model_version_id::text, COALESCE(b.source_name, ''), b.submitted_by::text, v.field_schema,
		       COALESCE(b.unknown_tag_policy, 'reject')
		FROM asset.import_batches b
		JOIN model.resource_model_versions v ON v.organization_id = b.organization_id AND v.id = b.resource_model_version_id
		WHERE b.id = $1::uuid AND b.status = 'processing'
	`, batchID).Scan(&header.id, &header.organizationID, &header.workspaceID, &header.modelID,
		&header.versionID, &header.sourceName, &header.submittedBy, &header.fieldSchema, &header.unknownTagPolicy); err != nil {
		return fmt.Errorf("load import job: %w", err)
	}
	rows, err := p.Store.Pool.Query(ctx, `
		SELECT id::text, row_number, source_row FROM asset.import_rows
		WHERE import_batch_id = $1::uuid
		  AND (status = 'pending' OR (status = 'processing' AND (lease_until IS NULL OR lease_until < now())))
		ORDER BY row_number
	`, batchID)
	if err != nil {
		return fmt.Errorf("load import rows: %w", err)
	}
	candidateRows := make([]pendingImportRow, 0)
	for rows.Next() {
		var row pendingImportRow
		if err := rows.Scan(&row.id, &row.number, &row.sourceRow); err != nil {
			rows.Close()
			return fmt.Errorf("scan import row: %w", err)
		}
		candidateRows = append(candidateRows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, row := range candidateRows {
		if err := p.processImportRow(ctx, header, row); err != nil {
			// Unexpected (non-validation) failure: park the row as rejected
			// with the cause, then keep going with the remaining rows.
			if rejectErr := p.rejectImportRow(ctx, row.id, row.number, err); rejectErr != nil {
				return fmt.Errorf("process import row %d: %v; mark row rejected: %w", row.number, err, rejectErr)
			}
		}
	}
	return p.completeImport(ctx, batchID)
}

// processImportRow claims one row (pending, or processing with an expired
// lease), validates it and, on success, creates the raw input, the asset with
// its first sealed imported version and the shared draft inside the row's
// transaction. Validation failures reject the row in the same transaction.
func (p TransferProcessor) processImportRow(ctx context.Context, header importBatchHeader, row pendingImportRow) error {
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var claimed string
	if err := tx.QueryRow(ctx, `
		UPDATE asset.import_rows
		SET status = 'processing', lease_owner = $2, lease_until = now() + interval '`+transferLeaseInterval+`',
		    attempt_count = attempt_count + 1
		WHERE id = $1::uuid
		  AND (status = 'pending' OR (status = 'processing' AND (lease_until IS NULL OR lease_until < now())))
		RETURNING id::text
	`, row.id, newLeaseOwner()).Scan(&claimed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Another run owns the row inside a live lease.
			return nil
		}
		return fmt.Errorf("claim import row: %w", err)
	}
	reject := func(entries []ImportRowError) error {
		return rejectImportRows(ctx, tx, row.id, entries)
	}
	var sourceRow map[string]any
	if err := json.Unmarshal(row.sourceRow, &sourceRow); err != nil {
		if err := reject([]ImportRowError{{Code: "invalid_json"}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// Structurally broken CSV rows arrive with handler-recorded findings under
	// a reserved key; they fail here so physical parse issues stay per-row.
	if preErrors := importPreRowErrors(sourceRow); len(preErrors) > 0 {
		if err := reject(preErrors); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// tag_keys is the only supported tag channel; the retired top-level tags
	// field fails the row instead of being silently migrated.
	if _, legacy := sourceRow[LegacyTagsField]; legacy {
		if err := reject([]ImportRowError{{Code: "legacy_tags_field_not_supported"}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	tagKeys, hasTagKeys, err := importRowTagKeys(sourceRow)
	if err != nil {
		if err := reject([]ImportRowError{{Code: "invalid_tag_keys", Message: err.Error()}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	title := stringPointer(sourceRow["title"])
	markdown := stringPointer(sourceRow["markdown"])
	fields := importFields(sourceRow)
	if err := ValidateContent(title, markdown, &fields); err != nil {
		if err := reject([]ImportRowError{{Code: "invalid_content"}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err := ValidateFields(header.fieldSchema, fields); err != nil {
		if err := reject([]ImportRowError{{Code: "invalid_fields"}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// Resolve tag_keys before any content is written: the batch policy decides
	// whether unknown keys reject the row or create the tags explicitly.
	actor := auth.Principal{OrganizationID: header.organizationID, UserID: header.submittedBy, UserType: "member"}
	var tagIDs []string
	if hasTagKeys && len(tagKeys) > 0 {
		if len(tagKeys) > MaxTagsPerDraft {
			if err := reject([]ImportRowError{{Code: "too_many_tags"}}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		tagIDs, err = p.resolveImportRowTags(ctx, tx, header, actor, tagKeys)
		if err != nil {
			var rowErrors []ImportRowError
			switch {
			case errors.Is(err, tag.ErrUnknownTag):
				rowErrors = []ImportRowError{{Code: "unknown_tag"}}
			case errors.Is(err, tag.ErrArchived):
				rowErrors = []ImportRowError{{Code: "tag_archived"}}
			case errors.Is(err, errImportTagCreateLimitExceeded):
				rowErrors = []ImportRowError{{Code: "import_tag_create_limit_exceeded"}}
			default:
				return err
			}
			if err := reject(rowErrors); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
	}
	payload, _ := json.Marshal(sourceRow)
	sum := sha256.Sum256(payload)
	var rawInputID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.raw_inputs
			(organization_id, workspace_id, submitted_by, source_type, content_type, payload, content_checksum)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'import', 'application/json', $4::jsonb, $5)
		RETURNING id::text
	`, header.organizationID, header.workspaceID, header.submittedBy, string(payload), hex.EncodeToString(sum[:])).Scan(&rawInputID); err != nil {
		return fmt.Errorf("create import raw input: %w", err)
	}
	assetID, versionID, versionNo, err := createAssetWithFirstVersionTx(ctx, tx,
		header.organizationID, header.workspaceID, header.modelID, header.versionID,
		rawInputID, OriginImported, derefString(title), "", derefString(markdown),
		fields, tagIDs, tag.SourceImport, header.submittedBy)
	if err != nil {
		return err
	}
	if err := emitAssetVersionCreatedTx(ctx, tx, &p.Events, header.organizationID, header.workspaceID, assetID, versionID, versionNo, actor); err != nil {
		return err
	}
	RecordAssetAuditTx(ctx, tx, header.organizationID, header.workspaceID, actor, "asset.import.created", assetID, map[string]any{
		"workspace_id": header.workspaceID,
		"batch_id":     header.id,
		"source_name":  header.sourceName,
		"row_number":   row.number,
		"version_id":   versionID,
	})
	if _, err := tx.Exec(ctx, `
		UPDATE asset.import_rows
		SET status = 'accepted', errors = '[]'::jsonb, last_error = NULL,
		    raw_input_id = $2::uuid, asset_id = $3::uuid, version_id = $4::uuid,
		    lease_owner = NULL, lease_until = NULL
		WHERE id = $1::uuid
	`, row.id, rawInputID, assetID, versionID); err != nil {
		return fmt.Errorf("accept import row: %w", err)
	}
	return tx.Commit(ctx)
}

// rejectImportRow parks a row whose short transaction failed outright.
func (p TransferProcessor) rejectImportRow(ctx context.Context, rowID string, rowNumber int, cause error) error {
	message := truncateTransferError(cause)
	if message == "" {
		message = "import row failed"
	}
	payload := mustJSON([]ImportRowError{{Code: "import_row_failed", Message: message}})
	if len(payload) == 0 {
		payload = []byte("[]")
	}
	if _, err := p.Store.Pool.Exec(ctx, rejectImportRowsSQL, rowID, string(payload), message); err != nil {
		return fmt.Errorf("reject import row %d: %w", rowNumber, err)
	}
	return nil
}

// completeImport recomputes the batch summary from the persisted row statuses
// and closes the batch, releasing the lease.
func (p TransferProcessor) completeImport(ctx context.Context, batchID string) error {
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE asset.import_batches b
		SET status = 'succeeded', completed_at = now(),
		    lease_owner = NULL, lease_until = NULL, last_error = NULL,
		    summary = (
		        SELECT jsonb_build_object(
		            'rows_total', count(*),
		            'rows_accepted', count(*) FILTER (WHERE r.status = 'accepted'),
		            'rows_rejected', count(*) FILTER (WHERE r.status = 'rejected'),
		            'rows_pending', count(*) FILTER (WHERE r.status IN ('pending', 'processing'))
		        )
		        FROM asset.import_rows r
		        WHERE r.import_batch_id = b.id
		    )
		WHERE b.id = $1::uuid AND b.status = 'processing'
	`, batchID); err != nil {
		return fmt.Errorf("complete import job: %w", err)
	}
	return tx.Commit(ctx)
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
	var organizationID, workspaceID, modelID, format string
	var snapshotRaw []byte
	if err := p.Store.Pool.QueryRow(ctx, `
		SELECT ej.organization_id::text, ej.workspace_id::text, ej.resource_model_id::text,
		       ej.query_snapshot, COALESCE(ej.format, 'jsonl')
		FROM asset.export_jobs ej
		WHERE ej.id = $1::uuid AND ej.status = 'processing'
	`, jobID).Scan(&organizationID, &workspaceID, &modelID, &snapshotRaw, &format); err != nil {
		return err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil {
		return fmt.Errorf("decode export snapshot: %w", err)
	}
	if format == "" {
		format = "jsonl"
	}
	// Tag filters resolve before the export query is built: unknown or
	// contradictory keys fail the job instead of silently exporting everything.
	filters, _ := snapshot["filters"].(map[string]any)
	tagFilter, err := p.resolveExportTagFilter(ctx, organizationID, workspaceID, filters)
	if err != nil {
		return err
	}
	query, args := exportAssetQuery(organizationID, workspaceID, modelID, snapshot, tagFilter)
	rows, err := p.Store.Pool.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	headers := []string{"id", "title", "markdown", "fields", "tag_keys", "visibility", "publication_status", "origin"}
	records := make([][]string, 0, 128)
	versionIDs := make([]string, 0, 128)
	for rows.Next() {
		var id, versionID, visibility, publication, origin string
		var title, markdown *string
		var fields []byte
		if err := rows.Scan(&id, &title, &markdown, &fields, &versionID, &visibility, &publication, &origin); err != nil {
			return err
		}
		records = append(records, []string{id, derefString(title), derefString(markdown), string(fields), "[]", visibility, publication, origin})
		versionIDs = append(versionIDs, versionID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// tag_keys come from one batched query over the exported versions — never a
	// per-row lookup.
	keysByVersion, err := p.loadExportTagKeys(ctx, organizationID, versionIDs)
	if err != nil {
		return err
	}
	for index, record := range records {
		record[4] = formatTagKeysCell(keysByVersion[versionIDs[index]])
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
				"fields": decodeJSONMap([]byte(record[3])), "tag_keys": decodeTagKeysCell(record[4]),
				"visibility": record[5], "publication_status": record[6], "origin": record[7],
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

// resolveExportTagFilter extracts and resolves the snapshot's
// tags_any/tags_all/tags_none key groups into workspace tag identities.
// Unknown keys and contradictions fail the export loudly.
func (p TransferProcessor) resolveExportTagFilter(ctx context.Context, organizationID, workspaceID string, filters map[string]any) (tag.ResolvedFilter, error) {
	if len(filters) == 0 {
		return tag.ResolvedFilter{}, nil
	}
	raw := tag.KeyFilter{}
	for name, target := range map[string]*[]string{
		"tags_any": &raw.Any, "tags_all": &raw.All, "tags_none": &raw.None,
	} {
		value, ok := filters[name]
		if !ok || value == nil {
			continue
		}
		items, isArray := value.([]any)
		if !isArray {
			return tag.ResolvedFilter{}, fmt.Errorf("%w: %s must be an array of keys", tag.ErrUnknownTag, name)
		}
		keys := make([]string, 0, len(items))
		for _, item := range items {
			key, isString := item.(string)
			if !isString {
				return tag.ResolvedFilter{}, fmt.Errorf("%w: %s entries must be strings", tag.ErrUnknownTag, name)
			}
			keys = append(keys, key)
		}
		*target = keys
	}
	normalized, err := tag.NormalizeFilter(raw)
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	resolve := func(keys []string) ([]string, error) {
		if len(keys) == 0 {
			return nil, nil
		}
		rows, err := p.Store.Pool.Query(ctx, `
			SELECT id::text FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = ANY($3::text[])
		`, organizationID, workspaceID, keys)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(ids) != len(keys) {
			return nil, tag.ErrUnknownTag
		}
		return ids, nil
	}
	anyIDs, err := resolve(normalized.Any)
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	allIDs, err := resolve(normalized.All)
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	noneIDs, err := resolve(normalized.None)
	if err != nil {
		return tag.ResolvedFilter{}, err
	}
	return tag.ResolvedFilter{Any: anyIDs, All: allIDs, None: noneIDs}, nil
}

// versionTagKeyPair is one scanned (asset_version_id, normalized_key) row.
type versionTagKeyPair struct {
	VersionID string
	Key       string
}

// mergeVersionTagKeys folds scanned pairs into a version→sorted normalized
// keys map; duplicate keys collapse.
func mergeVersionTagKeys(pairs []versionTagKeyPair) map[string][]string {
	result := make(map[string][]string, len(pairs))
	for _, pair := range pairs {
		list := result[pair.VersionID]
		if !containsString(list, pair.Key) {
			list = append(list, pair.Key)
			result[pair.VersionID] = list
		}
	}
	for versionID, keys := range result {
		sort.Strings(keys)
		result[versionID] = keys
	}
	return result
}

// formatTagKeysCell renders the sorted tag_keys cell used by CSV/XLSX exports:
// a JSON array text of normalized keys, deduplicated. Empty sets render as [].
func formatTagKeysCell(keys []string) string {
	sorted := make([]string, 0, len(keys))
	for _, key := range keys {
		if !containsString(sorted, key) {
			sorted = append(sorted, key)
		}
	}
	sort.Strings(sorted)
	return string(mustJSON(sorted))
}

// decodeTagKeysCell parses a tag_keys cell back into the string array the JSONL
// export embeds. Unparseable cells degrade to an empty array instead of
// failing the whole download.
func decodeTagKeysCell(raw string) []string {
	keys := []string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &keys)
	}
	if keys == nil {
		keys = []string{}
	}
	return keys
}

// loadExportTagKeys reads the normalized keys of many versions in one query,
// deduplicated and sorted per version.
func (p TransferProcessor) loadExportTagKeys(ctx context.Context, organizationID string, versionIDs []string) (map[string][]string, error) {
	if len(versionIDs) == 0 {
		return map[string][]string{}, nil
	}
	rows, err := p.Store.Pool.Query(ctx, `
		SELECT avt.asset_version_id::text, t.normalized_key
		FROM asset.asset_version_tags avt
		JOIN asset.tags t ON t.organization_id = avt.organization_id AND t.id = avt.tag_id
		WHERE avt.organization_id = $1::uuid AND avt.asset_version_id = ANY($2::uuid[])
	`, organizationID, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("load export tag keys: %w", err)
	}
	defer rows.Close()
	pairs := make([]versionTagKeyPair, 0, len(versionIDs))
	for rows.Next() {
		var pair versionTagKeyPair
		if err := rows.Scan(&pair.VersionID, &pair.Key); err != nil {
			return nil, err
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return mergeVersionTagKeys(pairs), nil
}

func exportAssetQuery(organizationID, workspaceID, modelID string, snapshot map[string]any, tagFilter tag.ResolvedFilter) (string, []any) {
	args := []any{organizationID, workspaceID, modelID}
	where := []string{
		"a.organization_id = $1::uuid",
		"a.workspace_id = $2::uuid",
		"a.resource_model_id = $3::uuid",
		"a.deleted_at IS NULL",
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
	for _, key := range []string{"visibility", "publication_status", "origin"} {
		if value, ok := filters[key].(string); ok && strings.TrimSpace(value) != "" {
			column := map[string]string{"visibility": "a.visibility", "publication_status": "a.publication_status", "origin": "v.origin"}[key]
			where = append(where, add(column+" = $%d", value))
		}
	}
	if fields, ok := filters["fields"].(map[string]any); ok && len(fields) > 0 {
		encoded, _ := json.Marshal(fields)
		where = append(where, add("v.fields @> $%d::jsonb", string(encoded)))
	}
	// Relational tag filters run EXISTS/NOT EXISTS against the selected version
	// pointer — the same fixed shape the facet and member list paths use.
	if len(tagFilter.Any) > 0 {
		where = append(where, add("EXISTS (SELECT 1 FROM asset.asset_version_tags fx WHERE fx.asset_version_id = v.id AND fx.tag_id = ANY($%d::uuid[]))", tagFilter.Any))
	}
	for _, id := range tagFilter.All {
		placeholder := add("SELECT $%d::uuid[]", []string{id})
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM asset.asset_version_tags fa%s WHERE fa%s.asset_version_id = v.id AND fa%s.tag_id = ANY(%s::uuid[]))", id, id, id, placeholder))
	}
	if len(tagFilter.None) > 0 {
		where = append(where, add("NOT EXISTS (SELECT 1 FROM asset.asset_version_tags fn WHERE fn.asset_version_id = v.id AND fn.tag_id = ANY($%d::uuid[]))", tagFilter.None))
	}
	query := "SELECT a.id::text, v.title, v.markdown, v.fields, v.id::text, a.visibility, a.publication_status, v.origin FROM asset.assets a JOIN asset.asset_versions v ON v.organization_id = a.organization_id AND v.id = COALESCE(a.current_published_version_id, a.current_working_version_id) WHERE " + strings.Join(where, " AND ") + " ORDER BY a.id"
	return query, args
}

func (p TransferProcessor) failImport(ctx context.Context, id string, cause error) error {
	_, err := p.Store.Pool.Exec(ctx, `UPDATE asset.import_batches SET status = 'failed', summary = jsonb_build_object('error', $2::text), last_error = $2, lease_owner = NULL, lease_until = NULL, completed_at = now() WHERE id = $1::uuid AND status = 'processing'`, id, truncateTransferError(cause))
	return err
}
func (p TransferProcessor) failExport(ctx context.Context, id string, cause error) error {
	_, err := p.Store.Pool.Exec(ctx, `UPDATE asset.export_jobs SET status = 'failed', error_code = $2, completed_at = now() WHERE id = $1::uuid AND status = 'processing'`, id, truncateTransferError(cause))
	return err
}

// ImportRowError is one per-row rejection finding persisted to
// asset.import_rows.errors for the failed-row report endpoints.
type ImportRowError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// rejectImportRowsSQL persists a rejected row. The errors payload is JSON built
// in Go and passed with an explicit ::jsonb cast on purpose: building it via
// jsonb_build_object('code', $2) makes $2 an argument of the variadic "any"
// function whose type PostgreSQL cannot infer at PREPARE time, so the first
// bad row failed the whole batch with SQLSTATE 42P18 "could not determine data
// type of parameter $2" instead of landing in import_rows.errors.
const rejectImportRowsSQL = `UPDATE asset.import_rows SET status = 'rejected', errors = $2::jsonb, last_error = $3, lease_owner = NULL, lease_until = NULL WHERE id = $1::uuid AND status IN ('pending', 'processing')`

func rejectImportRows(ctx context.Context, tx pgx.Tx, id string, entries []ImportRowError) error {
	if len(entries) == 0 {
		entries = []ImportRowError{{Code: "invalid_row"}}
	}
	payload := mustJSON(entries)
	if len(payload) == 0 {
		payload = []byte("[]")
	}
	message := entries[0].Code
	if entries[0].Message != "" {
		message = entries[0].Code + ": " + entries[0].Message
	}
	if _, err := tx.Exec(ctx, rejectImportRowsSQL, id, string(payload), message); err != nil {
		return fmt.Errorf("reject import row: %w", err)
	}
	return nil
}

// emitAssetVersionCreatedTx re-reads the lifecycle row (CreateVersionTx has
// already bumped its revision) and appends the asset.version_created fact.
func emitAssetVersionCreatedTx(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, organizationID, workspaceID, assetID, versionID string, versionNo int64, actor auth.Principal) error {
	row, err := LoadLifecycleTx(ctx, tx, organizationID, assetID)
	if err != nil {
		return err
	}
	return AppendAssetEventTx(ctx, tx, events, row, actor, eventing.EventAssetVersionCreated, eventing.PayloadVersionV1, eventing.AssetVersionCreatedPayload{
		AssetID:     assetID,
		VersionID:   versionID,
		VersionNo:   versionNo,
		WorkspaceID: workspaceID,
	})
}

// ImportPreRowErrorsKey is the reserved source-row key under which the imports
// endpoint records structural CSV findings (see frontend_transfer.go). Rows
// carrying it are rejected by the worker before field validation so physical
// parse issues never fail a whole batch.
const ImportPreRowErrorsKey = "__import_errors"

// importPreRowErrors extracts findings recorded by the imports endpoint when a
// physical CSV row could not be mapped cleanly (e.g. ragged field count). The
// reserved key is stripped before field extraction so it never leaks into the
// asset fields document.
func importPreRowErrors(sourceRow map[string]any) []ImportRowError {
	raw, ok := sourceRow[ImportPreRowErrorsKey]
	if !ok || raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return []ImportRowError{{Code: "invalid_row", Message: "malformed row error marker"}}
	}
	entries := make([]ImportRowError, 0, len(list))
	for _, item := range list {
		entry, _ := item.(map[string]any)
		code, _ := entry["code"].(string)
		if code == "" {
			code = "invalid_row"
		}
		message, _ := entry["message"].(string)
		entries = append(entries, ImportRowError{Code: code, Message: message})
	}
	if len(entries) == 0 {
		return []ImportRowError{{Code: "invalid_row"}}
	}
	return entries
}

// Row system fields: tag_keys is the supported tag channel; the legacy
// top-level tags field is rejected, never migrated and never leaks into the
// dynamic fields document.
const (
	ImportTagKeysField = "tag_keys"
	LegacyTagsField    = "tags" // retired input beside tag_keys; rejected per row
)

// errImportTagCreateLimitExceeded marks a row whose batch already created the
// allowed number of new tags under unknown_tag_policy=create.
var errImportTagCreateLimitExceeded = errors.New("import batch exceeded the created tag limit")

// resolveImportRowTags maps one row's tag_keys onto workspace tag identities
// inside the row transaction. The batch's frozen policy decides the channel:
// reject fails unknown/archived keys via tag.ResolveExisting semantics; create
// resolves through tag.CreateOrReuseTx and atomically charges the actual number
// of newly created tags against the batch's created_tag_count budget.
func (p TransferProcessor) resolveImportRowTags(ctx context.Context, tx pgx.Tx, header importBatchHeader, actor auth.Principal, tagKeys []string) ([]string, error) {
	ids := make([]string, 0, len(tagKeys))
	if header.unknownTagPolicy == UnknownTagPolicyCreate {
		created := 0
		for _, key := range tagKeys {
			resolved, err := tag.CreateOrReuseTx(ctx, tx, actor, header.workspaceID, key)
			if err != nil {
				return nil, err
			}
			ids = append(ids, resolved.ID)
			if resolved.Created {
				created++
			}
		}
		if created > 0 {
			result, err := tx.Exec(ctx, `
				UPDATE asset.import_batches
				SET created_tag_count = created_tag_count + $2
				WHERE id = $1::uuid AND created_tag_count + $2 <= $3
			`, header.id, created, maxImportCreatedTags)
			if err != nil {
				return nil, fmt.Errorf("charge created tags: %w", err)
			}
			if affected := result.RowsAffected(); affected == 0 {
				return nil, errImportTagCreateLimitExceeded
			}
		}
		return ids, nil
	}
	resolved, err := tag.ResolveExisting(ctx, p.Store, actor, header.workspaceID, tagKeys)
	if err != nil {
		return nil, err
	}
	for _, item := range resolved {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

// importRowTagKeys extracts the top-level tag_keys system field. ok reports
// whether the field was present; malformed payloads (non-array, non-string
// entries) fail with an error so the row lands in the rejected report.
func importRowTagKeys(row map[string]any) (keys []string, ok bool, err error) {
	raw, present := row[ImportTagKeysField]
	if !present || raw == nil {
		return nil, false, nil
	}
	list, isArray := raw.([]any)
	if !isArray {
		return nil, true, fmt.Errorf("%s must be a JSON array of strings", ImportTagKeysField)
	}
	keys = make([]string, 0, len(list))
	for _, item := range list {
		value, isString := item.(string)
		if !isString {
			return nil, true, fmt.Errorf("%s entries must be strings", ImportTagKeysField)
		}
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	return keys, true, nil
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
		// tag_keys and the legacy tags field are system fields: they resolve to
		// relations and never enter the dynamic fields document.
		if key == "title" || key == "markdown" || key == "source" || key == ImportPreRowErrorsKey ||
			key == ImportTagKeysField || key == LegacyTagsField {
			continue
		}
		result[key] = value
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
