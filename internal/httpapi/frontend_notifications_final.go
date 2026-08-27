package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type finalNotification struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	ObjectType  string         `json:"object_type,omitempty"`
	ObjectID    string         `json:"object_id,omitempty"`
	Metadata    map[string]any `json:"metadata"`
	ReadAt      *time.Time     `json:"read_at,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

func listNotificationsFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) || !rejectUnknownWorkspace(w, r, deps, principal) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "workspace.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		unreadOnly := r.URL.Query().Get("unread_only") == "true"
		rows, err := deps.Store.Pool.Query(r.Context(), `SELECT id::text, workspace_id::text, type, title, body, COALESCE(object_type, ''), COALESCE(object_id::text, ''), metadata, read_at, created_at FROM content.notifications WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid AND ($4 = false OR read_at IS NULL) ORDER BY created_at DESC, id DESC LIMIT $5`, principal.OrganizationID, workspaceID, principal.UserID, unreadOnly, limit+1)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_list_failed")
			return
		}
		defer rows.Close()
		items := make([]finalNotification, 0, limit)
		for rows.Next() {
			var item finalNotification
			var metadata []byte
			if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.Type, &item.Title, &item.Body, &item.ObjectType, &item.ObjectID, &metadata, &item.ReadAt, &item.CreatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "notifications_list_failed")
				return
			}
			item.Metadata = map[string]any{}
			_ = json.Unmarshal(metadata, &item.Metadata)
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_list_failed")
			return
		}
		hasMore := len(items) > limit
		if hasMore {
			items = items[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": hasMore})
	}
}

func unreadNotificationCountFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) || !rejectUnknownWorkspace(w, r, deps, principal) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "workspace.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		var count int64
		if err := deps.Store.Pool.QueryRow(r.Context(), `SELECT count(*) FROM content.notifications WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid AND read_at IS NULL`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&count); err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_count_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"unread_count": count})
	}
}

func markNotificationReadFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		if !requirePathUUID(w, r.PathValue("notificationId")) {
			return
		}
		result, err := deps.Store.Pool.Exec(r.Context(), `UPDATE content.notifications SET read_at = COALESCE(read_at, now()) WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND id = $3::uuid`, principal.OrganizationID, principal.UserID, r.PathValue("notificationId"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "notification_read_failed")
			return
		}
		if result.RowsAffected() == 0 {
			writeError(w, http.StatusNotFound, "notification_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "read"})
	}
}

func markAllNotificationsReadFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		if !requirePathUUID(w, r.PathValue("workspaceId")) || !rejectUnknownWorkspace(w, r, deps, principal) {
			return
		}
		if _, err := deps.Store.Pool.Exec(r.Context(), `UPDATE content.notifications SET read_at = COALESCE(read_at, now()) WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid AND read_at IS NULL`, principal.OrganizationID, r.PathValue("workspaceId"), principal.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_read_all_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func notificationStreamFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) || !rejectUnknownWorkspace(w, r, deps, principal) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "workspace.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		lastID, hasLastID := parseLastEventID(r)
		if hasLastID && lastID < 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_last_event_id")
			return
		}
		if !hasLastID {
			if err := deps.Store.Pool.QueryRow(r.Context(), `SELECT COALESCE(max(stream_id), 0) FROM content.notifications WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&lastID); err != nil {
				writeError(w, http.StatusInternalServerError, "notification_stream_failed")
				return
			}
		}
		setSSEHeaders(w)
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "stream_unavailable")
			return
		}
		if !hasLastID {
			fmt.Fprint(w, "event: reset\ndata: {}\n\n")
			flusher.Flush()
		}
		poll := time.NewTicker(time.Second)
		heartbeat := time.NewTicker(15 * time.Second)
		defer poll.Stop()
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-poll.C:
				nextID, err := streamNotifications(r.Context(), deps, principal.OrganizationID, workspaceID, principal.UserID, lastID, w)
				if err != nil {
					_ = writeSSE(w, flusher, "error", map[string]string{"code": "notification_stream_failed"})
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
}

func streamNotifications(ctx context.Context, deps Dependencies, organizationID, workspaceID, userID string, lastID int64, w http.ResponseWriter) (int64, error) {
	rows, err := deps.Store.Pool.Query(ctx, `
		SELECT stream_id, id::text, workspace_id::text, type, title, body,
		       COALESCE(object_type, ''), COALESCE(object_id::text, ''), metadata, read_at, created_at
		FROM content.notifications
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid AND stream_id > $4
		ORDER BY stream_id LIMIT 100
	`, organizationID, workspaceID, userID, lastID)
	if err != nil {
		return lastID, err
	}
	defer rows.Close()
	for rows.Next() {
		var streamID int64
		var item finalNotification
		var metadata []byte
		if err := rows.Scan(&streamID, &item.ID, &item.WorkspaceID, &item.Type, &item.Title, &item.Body, &item.ObjectType, &item.ObjectID, &metadata, &item.ReadAt, &item.CreatedAt); err != nil {
			return lastID, err
		}
		item.Metadata = map[string]any{}
		_ = json.Unmarshal(metadata, &item.Metadata)
		payload, err := json.Marshal(item)
		if err != nil {
			return lastID, err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: notification\ndata: %s\n\n", streamID, payload); err != nil {
			return lastID, err
		}
		lastID = streamID
	}
	return lastID, rows.Err()
}

func parseLastEventID(r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return -1, true
	}
	return value, true
}

func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}
