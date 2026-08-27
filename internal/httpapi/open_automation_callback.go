package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	agentquery "agentchunzhi/internal/query"

	"github.com/jackc/pgx/v5"
)

type automationCallbackRequest struct {
	Status       string         `json:"status"`
	Output       map[string]any `json:"output"`
	ErrorCode    string         `json:"error_code"`
	ErrorSummary string         `json:"error_summary"`
}

// automationRunCallback is the external Agent write-back boundary. The only
// credential accepted here is the short-lived token returned when the run is
// created; no member or database credential is accepted.
func automationRunCallback(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		runID := strings.TrimSpace(r.PathValue("runId"))
		if !agentquery.ValidUUID(runID) {
			writeError(w, http.StatusNotFound, "run_not_found")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			writeError(w, http.StatusUnauthorized, "run_credential_required")
			return
		}
		hash := sha256.Sum256([]byte(token))
		hashText := fmt.Sprintf("%x", hash[:])
		var input automationCallbackRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024)).Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		input.Status = strings.ToLower(strings.TrimSpace(input.Status))
		if input.Status != "succeeded" && input.Status != "failed" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_callback_status")
			return
		}
		if input.Output == nil {
			input.Output = map[string]any{}
		}
		output, _ := json.Marshal(input.Output)
		tx, err := deps.Store.Pool.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "callback_failed")
			return
		}
		defer tx.Rollback(r.Context())
		var organizationID, workspaceID, createdBy, currentStatus string
		var callbackExpiry string
		err = tx.QueryRow(r.Context(), `
			SELECT organization_id::text, workspace_id::text, created_by::text, status,
			       COALESCE(input_scope->>'_run_credential_expires_at', '')
			FROM automation.runs
			WHERE id = $1::uuid
			  AND input_scope->>'_run_credential_hash' = $2
			  AND NULLIF(input_scope->>'_run_credential_expires_at', '')::timestamptz > now()
                        FOR UPDATE`, runID, hashText).Scan(&organizationID, &workspaceID, &createdBy, &currentStatus, &callbackExpiry)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid_run_credential")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "callback_failed")
			return
		}
		if currentStatus == "succeeded" || currentStatus == "failed" || currentStatus == "canceled" {
			// Terminal-state idempotency must never answer 500 (P1-12). When
			// the late callback claims a DIFFERENT terminal state, record the
			// external claim inside runs.output_snapshot._late_callbacks so the
			// disagreement is auditable, then still answer 200 with the
			// authoritative server-side status plus a conflict_recorded flag.
			conflictRecorded := false
			if input.Status != currentStatus {
				entry := map[string]any{
					"claimed_status": input.Status,
					"output":         input.Output,
					"error_code":     input.ErrorCode,
					"error_summary":  input.ErrorSummary,
					"recorded_at":    time.Now().UTC().Format(time.RFC3339Nano),
				}
				payload, marshalErr := json.Marshal([]any{entry})
				if marshalErr == nil {
					if _, execErr := tx.Exec(r.Context(), `
						UPDATE automation.runs SET output_snapshot =
							jsonb_set(COALESCE(output_snapshot, '{}'::jsonb), '{_late_callbacks}',
								COALESCE(output_snapshot->'_late_callbacks', '[]'::jsonb) || $2::jsonb, true)
						WHERE id = $1::uuid`, runID, string(payload)); execErr == nil {
						conflictRecorded = true
					}
				}
			}
			_ = tx.Commit(r.Context())
			writeJSON(w, http.StatusOK, map[string]any{"status": currentStatus, "idempotent": true, "conflict_recorded": conflictRecorded, "credential_expires_at": callbackExpiry})
			return
		}
		status := input.Status
		if _, err := tx.Exec(r.Context(), `
			UPDATE automation.runs SET status = $2, progress = 100,
			       output_snapshot = $3::jsonb,
			       error_code = NULLIF($4, ''), error_summary = NULLIF($5, ''), completed_at = now()
                        WHERE id = $1::uuid AND status IN ('queued', 'running', 'cancel_requested')`, runID, status, string(output), input.ErrorCode, input.ErrorSummary); err != nil {
			writeError(w, http.StatusInternalServerError, "callback_failed")
			return
		}
		eventType := "run.succeeded"
		if status == "failed" {
			eventType = "run.failed"
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO automation.run_events (organization_id, run_id, event_type, payload) VALUES ($1::uuid, $2::uuid, $3, $4::jsonb)`, organizationID, runID, eventType, string(output)); err != nil {
			writeError(w, http.StatusInternalServerError, "callback_failed")
			return
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO audit.audit_log (organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata) VALUES ($1::uuid, $2::uuid, $3::uuid, 'automation.callback', 'task_run', $4::uuid, 'allowed', jsonb_build_object('status', $5::text, 'workspace_id', $6::text))`, organizationID, createdBy, createdBy, runID, status, workspaceID); err != nil {
			writeError(w, http.StatusInternalServerError, "callback_failed")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "callback_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "status": status})
	}
}
