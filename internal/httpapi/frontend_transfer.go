package httpapi

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
)

const maxImportBodyBytes = 8 * 1024 * 1024

func writeTransferError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, assetservice.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, tag.ErrCreatePermission):
		// unknown_tag_policy=create creates catalog resources on behalf of the
		// submitter and therefore demands tag.manage.
		writeError(w, http.StatusForbidden, "tag_manage_required")
	case errors.Is(err, assetservice.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, assetservice.ErrNotFound):
		writeError(w, http.StatusNotFound, "transfer_job_not_found")
	case errors.Is(err, assetservice.ErrConflict):
		writeError(w, http.StatusConflict, "transfer_conflict")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

// errEmptyImportPayload marks an import body that carries no usable rows
// (empty file or nothing but blank lines).
var errEmptyImportPayload = errors.New("empty import payload")

var csvNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func startImport(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
		if format != "" && format != "json" && format != "csv" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBodyBytes))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		input, ok := decodeImportPayload(format, r, raw)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		job, err := deps.TransferService.StartImport(r.Context(), principal, r.PathValue("workspaceId"), key, input)
		if err != nil {
			writeTransferError(w, err, "import_start_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	}
}

// decodeImportPayload parses the request body either as JSON rows (legacy
// behaviour) or as a CSV file. The explicit ?format=json|csv query parameter
// wins over content-type sniffing; sniffing falls back to a UTF-8 BOM and the
// leading character of the body ('{'/'[' means JSON).
func decodeImportPayload(format string, r *http.Request, raw []byte) (assetservice.ImportInput, bool) {
	payloadFormat := format
	if payloadFormat == "" {
		payloadFormat = detectImportFormat(r.Header.Get("Content-Type"), raw)
	}
	if payloadFormat == "csv" {
		rows, err := parseImportCSVRows(raw)
		if err != nil {
			return assetservice.ImportInput{}, false
		}
		query := r.URL.Query()
		return assetservice.ImportInput{
			ResourceModelID:        strings.TrimSpace(query.Get("resource_model_id")),
			ResourceModelVersionID: strings.TrimSpace(query.Get("resource_model_version_id")),
			SourceName:             strings.TrimSpace(query.Get("source_name")),
			Rows:                   rows,
		}, true
	}
	var input assetservice.ImportInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return assetservice.ImportInput{}, false
	}
	return input, true
}

func detectImportFormat(contentType string, body []byte) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mediaType {
	case "text/csv", "application/csv":
		return "csv"
	case "application/json":
		return "json"
	}
	trimmed := bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	trimmed = bytes.TrimLeft(trimmed, "\t\r\n ")
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return "json"
	}
	return "csv"
}

// parseImportCSVRows converts an uploaded CSV file into JSON row objects.
// The first non-empty line is the header; every data line becomes
// map[columnHeader]cellValue. Structural problems are tolerated per row where
// possible: ragged field counts stay visible via the reserved __import_errors
// marker which the import worker turns into a per-row rejection instead of a
// job-wide failure. Only file-level damage (unbalanced quotes) fails ingestion.
func parseImportCSVRows(payload []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimPrefix(payload, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(trimmed)) == 0 {
		return nil, errEmptyImportPayload
	}
	reader := csv.NewReader(bytes.NewReader(trimmed))
	// Field counts are validated row-by-row below; -1 disables csv.Reader's
	// whole-file ErrFieldCount abort so short/long lines stay recoverable.
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errEmptyImportPayload
		}
		return nil, fmt.Errorf("read import csv header: %w", err)
	}
	headers := make([]string, len(header))
	for index, name := range header {
		headers[index] = strings.TrimSpace(name)
	}
	rows := make([]map[string]any, 0, 64)
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read import csv row: %w", err)
		}
		if blankRecord(record) {
			continue
		}
		rows = append(rows, csvRecordToRow(headers, record))
	}
	if len(rows) == 0 {
		return nil, errEmptyImportPayload
	}
	return rows, nil
}

func blankRecord(record []string) bool {
	for _, value := range record {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func csvRecordToRow(headers, record []string) map[string]any {
	row := make(map[string]any, len(record))
	for index, value := range record {
		name := fmt.Sprintf("column_%d", index+1)
		if index < len(headers) && headers[index] != "" {
			name = headers[index]
		}
		row[name] = convertCSVCell(value)
	}
	if len(record) != len(headers) {
		row[assetservice.ImportPreRowErrorsKey] = []assetservice.ImportRowError{{
			Code:    "field_count",
			Message: fmt.Sprintf("expected %d fields, got %d", len(headers), len(record)),
		}}
	}
	return row
}

// convertCSVCell maps scalar CSV text onto JSON types the field schemas expect.
// Only unambiguous spellings are coerced; anything else stays a string so the
// worker records a precise per-row field rejection instead of guessing.
func convertCSVCell(raw string) any {
	cell := strings.TrimSpace(raw)
	if cell == "" {
		return raw
	}
	switch cell {
	case "true":
		return true
	case "false":
		return false
	}
	if looksCanonicalNumber(cell) {
		if number, err := strconv.ParseInt(cell, 10, 64); err == nil {
			return number
		}
		if number, err := strconv.ParseFloat(cell, 64); err == nil {
			return number
		}
	}
	if strings.HasPrefix(cell, "{") || strings.HasPrefix(cell, "[") {
		var parsed any
		if err := json.Unmarshal([]byte(cell), &parsed); err == nil {
			return parsed
		}
	}
	return raw
}

func looksCanonicalNumber(value string) bool {
	first := value[0]
	if (first < '0' || first > '9') && first != '-' {
		return false
	}
	return csvNumberPattern.MatchString(value)
}

func getImport(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("jobId")) {
			return
		}
		job, err := deps.TransferService.GetImport(r.Context(), principal, r.PathValue("jobId"))
		if err != nil {
			writeTransferError(w, err, "import_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, job)
	}
}

// listImportJobRows serves GET /api/frontend/import-jobs/{jobId}/rows with the
// same permission semantics as getImport, adding pagination over the persisted
// import_rows including rejected-row diagnostics.
func listImportJobRows(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		query := r.URL.Query()
		if !requirePathUUID(w, r.PathValue("jobId")) {
			return
		}
		onlyErrors, valid := parseOptionalBoolParam(query.Get("only_errors"))
		if !valid {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		limit := 50
		if value := strings.TrimSpace(query.Get("limit")); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 200 {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			limit = parsed
		}
		offset := 0
		if value := strings.TrimSpace(query.Get("offset")); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			offset = parsed
		}
		page, err := deps.TransferService.ListImportRows(r.Context(), principal, r.PathValue("jobId"), onlyErrors, limit, offset)
		if err != nil {
			writeTransferError(w, err, "import_rows_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}

// downloadImportJobErrorsCsv serves GET /api/frontend/import-jobs/{jobId}/errors.csv
// as a text/csv attachment with columns row_number,errors,data_json describing
// every rejected row of the batch.
func downloadImportJobErrorsCsv(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		jobID := r.PathValue("jobId")
		if !requirePathUUID(w, jobID) {
			return
		}
		items, err := deps.TransferService.ImportErrorRows(r.Context(), principal, jobID)
		if err != nil {
			writeTransferError(w, err, "import_error_rows_load_failed")
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "import-" + jobID + "-errors.csv"}))
		writer := csv.NewWriter(w)
		if err := writer.Write([]string{"row_number", "errors", "data_json"}); err != nil {
			return
		}
		for _, item := range items {
			if writer.Write([]string{strconv.Itoa(item.RowNumber), item.Errors, item.DataJSON}); err != nil {
				return
			}
		}
		writer.Flush()
	}
}

func parseOptionalBoolParam(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0":
		return false, true
	case "true", "1":
		return true, true
	}
	return false, false
}

func startExport(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input assetservice.ExportInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		job, err := deps.TransferService.StartExport(r.Context(), principal, r.PathValue("workspaceId"), key, input)
		if err != nil {
			writeTransferError(w, err, "export_start_failed")
			return
		}
		recordAuditAsync(deps, store.NewAuditEntry("asset.export.start", principal.OrganizationID, principal.UserID,
			"export_job", job.ID, map[string]any{
				"workspace_id":      r.PathValue("workspaceId"),
				"resource_model_id": input.ResourceModelID,
			}))
		writeJSON(w, http.StatusAccepted, job)
	}
}

func getExport(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("jobId")) {
			return
		}
		job, err := deps.TransferService.GetExport(r.Context(), principal, r.PathValue("jobId"))
		if err != nil {
			writeTransferError(w, err, "export_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, job)
	}
}
