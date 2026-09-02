package httpapi

import (
	"errors"
	"net/http"

	"agentchunzhi/internal/automation"
)

// automation.go — the task-run observability surface: members read one
// automation run, its attempts and cancel it. The scheduled-jobs management
// face and the external-task callback were retired with the 2026-09-02
// over-design sweep; prepare_asset runs are created by the prepare endpoint
// and agent tasks, react runs by the chat coordinator.

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
	default:
		writeError(w, http.StatusInternalServerError, fallback)
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
