package asset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/store"
	"github.com/jackc/pgx/v5"
)

type TransferService struct {
	Store  *store.Store
	Policy authz.WorkspacePolicy
}

type ImportInput struct {
	ResourceModelID        string           `json:"resource_model_id"`
	ResourceModelVersionID string           `json:"resource_model_version_id"`
	WorkspaceID            string           `json:"workspace_id"`
	Rows                   []map[string]any `json:"rows"`
	SourceName             string           `json:"source_name"`
}

type ExportInput struct {
	ResourceModelID string         `json:"resource_model_id"`
	Filters         map[string]any `json:"filters"`
	Format          string         `json:"format"`
}

type ImportJob struct {
	ID              string         `json:"id"`
	WorkspaceID     string         `json:"workspace_id"`
	ResourceModelID string         `json:"resource_model_id"`
	VersionID       string         `json:"resource_model_version_id"`
	Status          string         `json:"status"`
	Summary         map[string]any `json:"summary"`
	CreatedAt       time.Time      `json:"created_at"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	SourceName      string         `json:"source_name,omitempty"`
}

type ExportJob struct {
	ID              string     `json:"id"`
	WorkspaceID     string     `json:"workspace_id"`
	ResourceModelID string     `json:"resource_model_id"`
	Status          string     `json:"status"`
	Format          string     `json:"format"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	OutputObjectKey string     `json:"output_object_key,omitempty"`
	OutputSize      *int64     `json:"output_size,omitempty"`
	OutputChecksum  string     `json:"output_checksum,omitempty"`
}

func (s TransferService) require(ctx context.Context, principal auth.Principal, workspaceID, action string) error {
	if principal.UserType != "member" || s.Store == nil || s.Store.Pool == nil || s.Policy == nil {
		return ErrForbidden
	}
	_, err := s.Policy.Require(ctx, principal, workspaceID, "", action)
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return ErrForbidden
	}
	return err
}

func (s TransferService) StartImport(ctx context.Context, principal auth.Principal, workspaceID, idempotencyKey string, input ImportInput) (ImportJob, error) {
	if err := s.require(ctx, principal, workspaceID, "asset.write"); err != nil {
		return ImportJob{}, err
	}
	if input.WorkspaceID != "" && input.WorkspaceID != workspaceID {
		return ImportJob{}, ErrForbidden
	}
	if !validIdempotencyKey(idempotencyKey) || !validID(input.ResourceModelID) || !validID(input.ResourceModelVersionID) || len(input.Rows) == 0 || len(input.Rows) > 10000 {
		return ImportJob{}, ErrInvalidInput
	}
	var modelWorkspace string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM model.resource_models WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, input.ResourceModelID).Scan(&modelWorkspace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ImportJob{}, ErrNotFound
		}
		return ImportJob{}, err
	}
	if modelWorkspace != workspaceID {
		return ImportJob{}, ErrForbidden
	}
	var versionModel, versionStatus string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT resource_model_id::text, status FROM model.resource_model_versions WHERE id = $1::uuid`, input.ResourceModelVersionID).Scan(&versionModel, &versionStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ImportJob{}, ErrNotFound
		}
		return ImportJob{}, err
	}
	if versionModel != input.ResourceModelID {
		return ImportJob{}, ErrInvalidInput
	}
	if versionStatus != "published" {
		return ImportJob{}, fmt.Errorf("%w: import requires a published resource model version", ErrConflict)
	}
	payload, _ := json.Marshal(map[string]any{"source_name": input.SourceName, "rows": input.Rows})
	checksum := sha256.Sum256(payload)
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ImportJob{}, fmt.Errorf("begin import job: %w", err)
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `
                INSERT INTO asset.import_batches
                        (organization_id, workspace_id, resource_model_id, resource_model_version_id, submitted_by,
                         source_checksum, source_name, idempotency_key, status, summary)
                VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, 'queued', $9::jsonb)
                ON CONFLICT (organization_id, submitted_by, idempotency_key)
                WHERE idempotency_key IS NOT NULL DO NOTHING
                RETURNING id::text
        `, principal.OrganizationID, workspaceID, input.ResourceModelID, input.ResourceModelVersionID,
		principal.UserID, hex.EncodeToString(checksum[:]), input.SourceName, idempotencyKey,
		mustJSON(map[string]any{"rows_total": len(input.Rows), "rows_accepted": 0, "rows_rejected": 0})).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var storedChecksum string
		if err := tx.QueryRow(ctx, `SELECT id::text, source_checksum FROM asset.import_batches WHERE organization_id = $1::uuid AND submitted_by = $2::uuid AND idempotency_key = $3`, principal.OrganizationID, principal.UserID, idempotencyKey).Scan(&id, &storedChecksum); err != nil {
			return ImportJob{}, fmt.Errorf("load idempotent import job: %w", err)
		}
		if storedChecksum != hex.EncodeToString(checksum[:]) {
			return ImportJob{}, ErrConflict
		}
	} else if err != nil {
		return ImportJob{}, fmt.Errorf("create import job: %w", err)
	} else {
		for index, row := range input.Rows {
			rowPayload, marshalErr := json.Marshal(row)
			if marshalErr != nil {
				return ImportJob{}, fmt.Errorf("encode import row %d: %w", index+1, marshalErr)
			}
			rowChecksum := sha256.Sum256(rowPayload)
			// Deterministic per-row key derived from the batch id and row
			// number so replayed batches cannot duplicate a row insert.
			keySum := sha256.Sum256([]byte(id + ":" + strconv.Itoa(index+1)))
			if _, execErr := tx.Exec(ctx, `
                                INSERT INTO asset.import_rows (import_batch_id, row_number, source_row, row_checksum, idempotency_key)
                                VALUES ($1::uuid, $2, $3::jsonb, $4, $5)
                        `, id, index+1, string(rowPayload), hex.EncodeToString(rowChecksum[:]), hex.EncodeToString(keySum[:])[:32]); execErr != nil {
				return ImportJob{}, fmt.Errorf("persist import row %d: %w", index+1, execErr)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ImportJob{}, fmt.Errorf("commit import job: %w", err)
	}
	return s.GetImport(ctx, principal, id)
}

func (s TransferService) GetImport(ctx context.Context, principal auth.Principal, jobID string) (ImportJob, error) {
	if err := s.authorizeImportRead(ctx, principal, jobID); err != nil {
		return ImportJob{}, err
	}
	var item ImportJob
	var summary []byte
	err := s.Store.Pool.QueryRow(ctx, `SELECT ib.id::text, ib.workspace_id::text, ib.resource_model_id::text, ib.resource_model_version_id::text, ib.status, ib.summary, ib.source_name, ib.created_at, ib.completed_at FROM asset.import_batches ib WHERE ib.organization_id = $1::uuid AND ib.id = $2::uuid`, principal.OrganizationID, jobID).Scan(&item.ID, &item.WorkspaceID, &item.ResourceModelID, &item.VersionID, &item.Status, &summary, &item.SourceName, &item.CreatedAt, &item.CompletedAt)
	item.Summary = decodeJSONMap(summary)
	return item, err
}

// authorizeImportRead applies the same checks as GetImport: organization
// scoping plus asset.read on the owning workspace. Unknown IDs surface as
// ErrNotFound so callers can keep their existing not-found semantics.
func (s TransferService) authorizeImportRead(ctx context.Context, principal auth.Principal, jobID string) error {
	if !validID(jobID) {
		return ErrInvalidInput
	}
	var workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT ib.workspace_id::text FROM asset.import_batches ib WHERE ib.organization_id = $1::uuid AND ib.id = $2::uuid`, principal.OrganizationID, jobID).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return s.require(ctx, principal, workspaceID, "asset.read")
}

const (
	defaultImportRowsLimit = 50
	maxImportRowsLimit     = 200
)

// ImportJobRow is one import_rows entry for the failed-row report listing.
type ImportJobRow struct {
	RowNumber int              `json:"row_number"`
	Status    string           `json:"status"`
	Errors    []map[string]any `json:"errors,omitempty"`
	Data      map[string]any   `json:"data"`
}

// ImportJobRowsPage is the paginated response payload of the rows listing.
type ImportJobRowsPage struct {
	JobID         string         `json:"job_id"`
	TotalRows     int            `json:"total_rows"`
	TotalRejected int            `json:"total_rejected"`
	Limit         int            `json:"limit"`
	Offset        int            `json:"offset"`
	HasMore       bool           `json:"has_more"`
	Items         []ImportJobRow `json:"items"`
}

// ImportErrorRow carries the raw persisted text of a rejected row so the CSV
// download can stream byte-identical source data.
type ImportErrorRow struct {
	RowNumber int    `json:"row_number"`
	Status    string `json:"status"`
	Errors    string `json:"errors"`    // errors jsonb rendered as text, e.g. [{"code":"invalid_fields"}]
	DataJSON  string `json:"data_json"` // original source_row jsonb as text
}

const importRowsJoinClause = `
	FROM asset.import_rows r
	JOIN asset.import_batches b ON b.id = r.import_batch_id
	WHERE b.organization_id = $1::uuid AND b.id = $2::uuid`

// ListImportRows returns a page of import_rows entries for an import batch,
// optionally restricted to rejected ("error") rows.
func (s TransferService) ListImportRows(ctx context.Context, principal auth.Principal, jobID string, onlyErrors bool, limit, offset int) (ImportJobRowsPage, error) {
	if limit <= 0 {
		limit = defaultImportRowsLimit
	}
	if limit > maxImportRowsLimit {
		limit = maxImportRowsLimit
	}
	if offset < 0 {
		offset = 0
	}
	page := ImportJobRowsPage{JobID: jobID, Limit: limit, Offset: offset, Items: make([]ImportJobRow, 0)}
	if err := s.authorizeImportRead(ctx, principal, jobID); err != nil {
		return ImportJobRowsPage{}, err
	}
	if err := s.Store.Pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE r.status = 'rejected')`+importRowsJoinClause,
		principal.OrganizationID, jobID).Scan(&page.TotalRows, &page.TotalRejected); err != nil {
		return ImportJobRowsPage{}, fmt.Errorf("count import rows: %w", err)
	}
	filter := ""
	if onlyErrors {
		filter = " AND r.status = 'rejected'"
	}
	rows, err := s.Store.Pool.Query(ctx,
		`SELECT r.row_number, r.status, r.errors::text, r.source_row::text`+importRowsJoinClause+filter+
			` ORDER BY r.row_number LIMIT $3 OFFSET $4`,
		principal.OrganizationID, jobID, limit, offset)
	if err != nil {
		return ImportJobRowsPage{}, fmt.Errorf("load import rows: %w", err)
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		matched++
		var item ImportJobRow
		var rawErrors, rawData []byte
		if err := rows.Scan(&item.RowNumber, &item.Status, &rawErrors, &rawData); err != nil {
			return ImportJobRowsPage{}, fmt.Errorf("scan import row: %w", err)
		}
		item.Errors = decodeImportRowErrors(rawErrors)
		item.Data = decodeJSONMap(rawData)
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return ImportJobRowsPage{}, err
	}
	total := page.TotalRows
	if onlyErrors {
		total = page.TotalRejected
	}
	page.HasMore = offset+len(page.Items) < total
	return page, nil
}

// ImportErrorRows returns all rejected rows ordered by row number for the
// failed-row CSV download.
func (s TransferService) ImportErrorRows(ctx context.Context, principal auth.Principal, jobID string) ([]ImportErrorRow, error) {
	if err := s.authorizeImportRead(ctx, principal, jobID); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx,
		`SELECT r.row_number, r.status, r.errors::text, r.source_row::text`+importRowsJoinClause+
			` AND r.status = 'rejected' ORDER BY r.row_number`,
		principal.OrganizationID, jobID)
	if err != nil {
		return nil, fmt.Errorf("load import error rows: %w", err)
	}
	defer rows.Close()
	items := make([]ImportErrorRow, 0)
	for rows.Next() {
		var item ImportErrorRow
		if err := rows.Scan(&item.RowNumber, &item.Status, &item.Errors, &item.DataJSON); err != nil {
			return nil, fmt.Errorf("scan import error row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeImportRowErrors(raw []byte) []map[string]any {
	result := make([]map[string]any, 0)
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil {
		return make([]map[string]any, 0)
	}
	return result
}

func decodeJSONMap(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

func (s TransferService) StartExport(ctx context.Context, principal auth.Principal, workspaceID, idempotencyKey string, input ExportInput) (ExportJob, error) {
	if err := s.require(ctx, principal, workspaceID, "asset.read"); err != nil {
		return ExportJob{}, err
	}
	if !validIdempotencyKey(idempotencyKey) || !validID(input.ResourceModelID) {
		return ExportJob{}, ErrInvalidInput
	}
	if input.Format == "" {
		input.Format = "jsonl"
	}
	if input.Format != "jsonl" && input.Format != "csv" && input.Format != "xlsx" {
		return ExportJob{}, ErrInvalidInput
	}
	var modelWorkspace string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM model.resource_models WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, input.ResourceModelID).Scan(&modelWorkspace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ExportJob{}, ErrNotFound
		}
		return ExportJob{}, err
	}
	if modelWorkspace != workspaceID {
		return ExportJob{}, ErrForbidden
	}
	snapshot, _ := json.Marshal(map[string]any{"resource_model_id": input.ResourceModelID, "filters": input.Filters, "format": input.Format})
	scope, _ := json.Marshal(map[string]any{"workspace_id": workspaceID, "member_id": principal.UserID})
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ExportJob{}, fmt.Errorf("begin export job: %w", err)
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `
                INSERT INTO asset.export_jobs
                        (organization_id, workspace_id, resource_model_id, submitted_by, idempotency_key, status, query_snapshot, permission_scope, format)
                VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 'queued', $6::jsonb, $7::jsonb, $8)
                ON CONFLICT (organization_id, submitted_by, idempotency_key)
                WHERE idempotency_key IS NOT NULL DO NOTHING
                RETURNING id::text
        `, principal.OrganizationID, workspaceID, input.ResourceModelID, principal.UserID, idempotencyKey, string(snapshot), string(scope), input.Format).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var storedSnapshot []byte
		if err := tx.QueryRow(ctx, `SELECT id::text, query_snapshot FROM asset.export_jobs WHERE organization_id = $1::uuid AND submitted_by = $2::uuid AND idempotency_key = $3`, principal.OrganizationID, principal.UserID, idempotencyKey).Scan(&id, &storedSnapshot); err != nil {
			return ExportJob{}, fmt.Errorf("load idempotent export job: %w", err)
		}
		if string(storedSnapshot) != string(snapshot) {
			return ExportJob{}, ErrConflict
		}
	} else if err != nil {
		return ExportJob{}, fmt.Errorf("create export job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ExportJob{}, fmt.Errorf("commit export job: %w", err)
	}
	return s.GetExport(ctx, principal, id)
}

func (s TransferService) GetExport(ctx context.Context, principal auth.Principal, jobID string) (ExportJob, error) {
	if !validID(jobID) {
		return ExportJob{}, ErrInvalidInput
	}
	var workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT ej.workspace_id::text FROM asset.export_jobs ej WHERE ej.organization_id = $1::uuid AND ej.id = $2::uuid`, principal.OrganizationID, jobID).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ExportJob{}, ErrNotFound
		}
		return ExportJob{}, err
	}
	if err := s.require(ctx, principal, workspaceID, "asset.read"); err != nil {
		return ExportJob{}, err
	}
	var item ExportJob
	err := s.Store.Pool.QueryRow(ctx, `SELECT ej.id::text, ej.workspace_id::text, ej.resource_model_id::text, ej.status, COALESCE(ej.format, 'jsonl'), COALESCE(ej.output_object_key, ''), ej.output_size, COALESCE(ej.output_checksum, ''), ej.created_at, ej.completed_at FROM asset.export_jobs ej WHERE ej.organization_id = $1::uuid AND ej.id = $2::uuid`, principal.OrganizationID, jobID).Scan(&item.ID, &item.WorkspaceID, &item.ResourceModelID, &item.Status, &item.Format, &item.OutputObjectKey, &item.OutputSize, &item.OutputChecksum, &item.CreatedAt, &item.CompletedAt)
	return item, err
}
