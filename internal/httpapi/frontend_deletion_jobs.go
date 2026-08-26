package httpapi

import (
	"errors"
	"net/http"

	"agentchunzhi/internal/deletion"
)

func getDeletionJob(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		job, err := (deletion.Service{Store: deps.Store}).Get(r.Context(), principal, r.PathValue("jobId"))
		if errors.Is(err, deletion.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deletion_job_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "deletion_job_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, job)
	}
}
