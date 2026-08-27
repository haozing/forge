package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/store"
)

func taskRunEventsFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		runID := r.PathValue("runId")
		if !requirePathUUID(w, runID) {
			return
		}
		var workspaceID string
		if err := deps.Store.Pool.QueryRow(r.Context(), `SELECT workspace_id::text FROM automation.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, runID).Scan(&workspaceID); err != nil {
			writeError(w, http.StatusNotFound, "task_run_not_found")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "automation.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		taskRunEventsAuthorized(deps, principal.OrganizationID, w, r)
	}
}

func taskRunEventsAuthorized(deps Dependencies, organizationID string, w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	lastID, hasLastID := parseLastEventID(r)
	if hasLastID && lastID < 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_last_event_id")
		return
	}
	setSSEHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unavailable")
		return
	}
	poll := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	if nextID, err := streamTaskRunEvents(r.Context(), deps, organizationID, runID, lastID, w); err != nil {
		_ = writeSSE(w, flusher, "error", map[string]string{"code": "task_run_events_failed"})
		return
	} else {
		lastID = nextID
		flusher.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			nextID, err := streamTaskRunEvents(r.Context(), deps, organizationID, runID, lastID, w)
			if err != nil {
				_ = writeSSE(w, flusher, "error", map[string]string{"code": "task_run_events_failed"})
				return
			}
			if nextID != lastID {
				lastID = nextID
				flusher.Flush()
			}
		case <-heartbeat.C:
			fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n")
			flusher.Flush()
		}
	}
}

func streamTaskRunEvents(ctx context.Context, deps Dependencies, organizationID, runID string, lastID int64, w http.ResponseWriter) (int64, error) {
	rows, err := deps.Store.Pool.Query(ctx, `SELECT id, event_type, payload FROM automation.run_events WHERE organization_id = $1::uuid AND run_id = $2::uuid AND id > $3 ORDER BY id LIMIT 500`, organizationID, runID, lastID)
	if err != nil {
		return lastID, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var typ string
		var payload []byte
		if err := rows.Scan(&id, &typ, &payload); err != nil {
			return lastID, err
		}
		if len(payload) == 0 {
			payload = []byte(`{}`)
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, typ, payload); err != nil {
			return lastID, err
		}
		lastID = id
	}
	return lastID, rows.Err()
}

func exportDownloadFinal(deps Dependencies) http.HandlerFunc {
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
		var workspaceID, status, objectKey, contentType string
		var size *int64
		err := deps.Store.Pool.QueryRow(r.Context(), `SELECT rm.workspace_id::text, e.status, COALESCE(e.output_object_key, ''), COALESCE(e.output_content_type, 'application/octet-stream'), e.output_size FROM asset.export_jobs e JOIN model.resource_models rm ON rm.id = e.resource_model_id AND rm.organization_id = e.organization_id WHERE e.organization_id = $1::uuid AND e.id = $2::uuid`, principal.OrganizationID, r.PathValue("jobId")).Scan(&workspaceID, &status, &objectKey, &contentType, &size)
		if err != nil {
			writeError(w, http.StatusNotFound, "export_job_not_found")
			return
		}
		if status != "succeeded" {
			writeError(w, http.StatusConflict, "export_not_ready")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "asset.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		if objectKey == "" || deps.AttachmentService.Objects == nil {
			writeError(w, http.StatusNotFound, "export_output_not_found")
			return
		}
		object, err := deps.AttachmentService.Objects.Get(r.Context(), objectKeyRef(objectKey))
		if err != nil {
			writeError(w, http.StatusNotFound, "export_output_not_found")
			return
		}
		defer object.Body.Close()
		w.Header().Set("Content-Type", contentType)
		extension := "bin"
		switch contentType {
		case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
			extension = "xlsx"
		case "text/csv":
			extension = "csv"
		case "application/x-ndjson", "application/json":
			extension = "jsonl"
		default:
			if strings.HasSuffix(objectKey, ".xlsx") {
				extension = "xlsx"
			} else if strings.HasSuffix(objectKey, ".csv") {
				extension = "csv"
			}
		}
		w.Header().Set("Content-Disposition", "attachment; filename=export-"+r.PathValue("jobId")+"."+extension)
		if size != nil {
			w.Header().Set("Content-Length", strconv.FormatInt(*size, 10))
		}
		_, _ = io.Copy(w, object.Body)
		recordAuditAsync(deps, store.NewAuditEntry("asset.export.download", principal.OrganizationID, principal.UserID,
			"export_job", r.PathValue("jobId"), map[string]any{
				"workspace_id": workspaceID,
			}))
	}
}

func objectKeyRef(key string) objectstore.ObjectRef { return objectstore.ObjectRef{Key: key} }
