package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	assetservice "agentchunzhi/internal/asset"
)

func writeTransferError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, assetservice.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
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
		key, ok := requiredIdempotencyKey(r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input assetservice.ImportInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
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
		job, err := deps.TransferService.GetImport(r.Context(), principal, r.PathValue("jobId"))
		if err != nil {
			writeTransferError(w, err, "import_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, job)
	}
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
		key, ok := requiredIdempotencyKey(r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
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
		job, err := deps.TransferService.GetExport(r.Context(), principal, r.PathValue("jobId"))
		if err != nil {
			writeTransferError(w, err, "export_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, job)
	}
}
