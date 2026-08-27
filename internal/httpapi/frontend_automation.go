package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"agentchunzhi/internal/automation"
)

func writeAutomationError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, automation.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, automation.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, automation.ErrNotFound):
		writeError(w, http.StatusNotFound, "automation_not_found")
	case errors.Is(err, automation.ErrConflict):
		writeError(w, http.StatusConflict, "automation_conflict")
	// P1-11: precise reasons for the composite job-creation constraints,
	// replacing the bare workspace_access_denied they used to collapse into.
	case errors.Is(err, automation.ErrAppNotBound):
		writeError(w, http.StatusForbidden, "application_not_bound_to_workspace")
	case errors.Is(err, automation.ErrAppDisabled):
		writeError(w, http.StatusForbidden, "agent_application_disabled")
	case errors.Is(err, automation.ErrWorkflowMismatch):
		writeError(w, http.StatusUnprocessableEntity, "workflow_mismatch")
	case errors.Is(err, automation.ErrEndpointUnavailable):
		writeError(w, http.StatusForbidden, "model_endpoint_unavailable")
	case errors.Is(err, automation.ErrRevokedRevision):
		writeError(w, http.StatusForbidden, "model_endpoint_revision_revoked")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func listAutomationJobs(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.AutomationService.ListJobs(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeAutomationError(w, err, "automation_job_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}
func createAutomationJob(deps Dependencies) http.HandlerFunc {
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
		var input automation.CreateJobInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		item, err := deps.AutomationService.CreateJob(r.Context(), principal, r.PathValue("workspaceId"), key, input)
		if err != nil {
			writeAutomationError(w, err, "automation_job_create_failed")
			return
		}
		writeJSON(w, http.StatusCreated, item)
	}
}
func automationJobsCollection(deps Dependencies) http.HandlerFunc {
	list := listAutomationJobs(deps)
	create := createAutomationJob(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list(w, r)
			return
		}
		create(w, r)
	}
}
func patchAutomationJob(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
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
		_ = key
		var input automation.PatchJobInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if !requirePathUUID(w, r.PathValue("jobId")) {
			return
		}
		item, err := deps.AutomationService.PatchJob(r.Context(), principal, r.PathValue("jobId"), input)
		if err != nil {
			writeAutomationError(w, err, "automation_job_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}
func getAutomationJob(deps Dependencies) http.HandlerFunc {
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
		item, err := deps.AutomationService.GetJob(r.Context(), principal, r.PathValue("jobId"))
		if err != nil {
			writeAutomationError(w, err, "automation_job_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}
func automationJobResource(deps Dependencies) http.HandlerFunc {
	get := getAutomationJob(deps)
	patch := patchAutomationJob(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		patch(w, r)
	}
}
func pauseAutomationJob(deps Dependencies, enabled bool) http.HandlerFunc {
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
		_ = key
		if !requirePathUUID(w, r.PathValue("jobId")) {
			return
		}
		item, err := deps.AutomationService.PatchJob(r.Context(), principal, r.PathValue("jobId"), automation.PatchJobInput{Enabled: &enabled})
		if err != nil {
			writeAutomationError(w, err, "automation_job_state_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}
func listAutomationRuns(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createAutomationRun(deps)(w, r)
			return
		}
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
		items, err := deps.AutomationService.ListRuns(r.Context(), principal, r.PathValue("jobId"))
		if err != nil {
			writeAutomationError(w, err, "automation_run_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func createAutomationRun(deps Dependencies) http.HandlerFunc {
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
		source := "manual"
		if raw := r.URL.Query().Get("source"); raw != "" {
			source = raw
		}
		if !requirePathUUID(w, r.PathValue("jobId")) {
			return
		}
		item, err := deps.AutomationService.CreateRun(r.Context(), principal, r.PathValue("jobId"), key, source)
		if err != nil {
			writeAutomationError(w, err, "automation_run_create_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	}
}
func getAutomationRun(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		item, err := deps.AutomationService.GetRun(r.Context(), principal, r.PathValue("runId"))
		if err != nil {
			writeAutomationError(w, err, "automation_run_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func listAutomationAttempts(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		items, err := deps.AutomationService.ListAttempts(r.Context(), principal, r.PathValue("runId"))
		if err != nil {
			writeAutomationError(w, err, "automation_attempt_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}
func cancelAutomationRun(deps Dependencies) http.HandlerFunc {
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
		_ = key
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		item, err := deps.AutomationService.CancelRun(r.Context(), principal, r.PathValue("runId"))
		if err != nil {
			writeAutomationError(w, err, "automation_run_cancel_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}
func retryAutomationRun(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		item, err := deps.AutomationService.RetryRun(r.Context(), principal, r.PathValue("runId"), key)
		if err != nil {
			writeAutomationError(w, err, "automation_run_retry_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	}
}
