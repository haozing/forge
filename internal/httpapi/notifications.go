package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

// notificationRow is the raw storage shape. The list and the SSE stream read
// exactly these columns and render through presentNotification, so the two
// faces can never drift again — the stream query once selected six columns
// (type/title/body/object_type/object_id/metadata) that do not exist in
// content.notifications and failed on every poll against a real database.
type notificationRow struct {
	streamID    int64
	id          string
	workspaceID string
	kind        string
	payload     []byte
	readAt      *time.Time
	createdAt   time.Time
}

const notificationColumns = "stream_id, id::text, workspace_id::text, kind, payload, read_at, created_at"

// notificationFallbacks carries the presentation copy for historical rows
// written before payloads carried title/body; kind is the lookup key.
var notificationFallbacks = map[string][2]string{
	"publication.submitted":        {"有新内容待审核", "一条内容提交了发布申请，等待审核。"},
	"publication.approved":         {"审核通过", "你提交的内容已通过审核并发布。"},
	"publication.rejected":         {"审核驳回", "你提交的内容未通过审核，请查看驳回意见。"},
	"publication.cancelled":        {"发布申请已取消", "发布申请因内容更新或归档被自动取消。"},
	"publication.scheduled_failed": {"定时发布失败", "定时发布未能执行，请重新发布。"},
	"system":                       {"系统通知", ""},
}

// payloadContract documents the agreed payload keys every notification
// writer must set (title/body required, object pair recommended). The
// writer-side copies live next to each INSERT INTO content.notifications.
//
//	title        string  — list/stream headline
//	body         string  — summary line
//	object_type  string  — routing hint for deep links
//	object_id    string  — related object id
//	...kind-specific business fields ride along unchanged.

func metadataText(metadata map[string]any, key, fallback string) string {
	if value, ok := metadata[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

// presentNotification renders one storage row into the wire shape: payload
// title/body/object win, historical rows fall back to the kind's copy.
func presentNotification(row notificationRow) finalNotification {
	item := finalNotification{
		ID:          row.id,
		WorkspaceID: row.workspaceID,
		Type:        row.kind,
		ReadAt:      row.readAt,
		CreatedAt:   row.createdAt,
		Metadata:    map[string]any{},
	}
	_ = json.Unmarshal(row.payload, &item.Metadata)
	fallback := notificationFallbacks[row.kind]
	item.Title = metadataText(item.Metadata, "title", fallback[0])
	if item.Title == "" {
		item.Title = row.kind
	}
	item.Body = metadataText(item.Metadata, "body", fallback[1])
	item.ObjectType = metadataText(item.Metadata, "object_type", "")
	item.ObjectID = metadataText(item.Metadata, "object_id", "")
	return item
}

func listNotifications(deps Dependencies) http.HandlerFunc {
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
		rows, err := deps.Store.Pool.Query(r.Context(), `SELECT `+notificationColumns+` FROM content.notifications WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid AND ($4 = false OR read_at IS NULL) ORDER BY stream_id DESC LIMIT $5`, principal.OrganizationID, workspaceID, principal.UserID, unreadOnly, limit+1)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_list_failed")
			return
		}
		defer rows.Close()
		items := make([]finalNotification, 0, limit)
		for rows.Next() {
			var row notificationRow
			if err := rows.Scan(&row.streamID, &row.id, &row.workspaceID, &row.kind, &row.payload, &row.readAt, &row.createdAt); err != nil {
				writeError(w, http.StatusInternalServerError, "notifications_list_failed")
				return
			}
			items = append(items, presentNotification(row))
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

// listAllNotifications serves GET /api/notifications: the recipient's
// notifications across every workspace of the organization — the global
// (topbar) surface the per-workspace list cannot answer. The cursor is
// the decimal stream_id of the last item of the previous page.
func listAllNotifications(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		unreadOnly := r.URL.Query().Get("unread_only") == "true"
		cursorParam := strings.TrimSpace(r.URL.Query().Get("cursor"))
		var cursor int64
		if cursorParam != "" {
			var err error
			if cursor, err = strconv.ParseInt(cursorParam, 10, 64); err != nil || cursor < 0 {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
		}
		filter := ""
		if cursor > 0 {
			filter += fmt.Sprintf(" AND stream_id < %d", cursor)
		}
		if unreadOnly {
			filter += " AND read_at IS NULL"
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `SELECT `+notificationColumns+` FROM content.notifications WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid`+filter+` ORDER BY stream_id DESC LIMIT `+strconv.Itoa(limit+1), principal.OrganizationID, principal.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_list_failed")
			return
		}
		defer rows.Close()
		items := make([]finalNotification, 0, limit+1)
		var hasMore bool
		var lastStreamID int64
		for rows.Next() {
			var row notificationRow
			if err := rows.Scan(&row.streamID, &row.id, &row.workspaceID, &row.kind, &row.payload, &row.readAt, &row.createdAt); err != nil {
				writeError(w, http.StatusInternalServerError, "notifications_list_failed")
				return
			}
			if len(items) == limit {
				// The extra row only proves more pages exist; the cursor must
				// stay on the last *returned* row or this row would be skipped
				// by the next page's strict `stream_id < cursor`.
				hasMore = true
				break
			}
			items = append(items, presentNotification(row))
			lastStreamID = row.streamID
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_list_failed")
			return
		}
		payload := map[string]any{"items": items, "has_more": hasMore}
		if hasMore && lastStreamID > 0 {
			payload["next_cursor"] = strconv.FormatInt(lastStreamID, 10)
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func unreadNotificationCount(deps Dependencies) http.HandlerFunc {
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

func markNotificationRead(deps Dependencies) http.HandlerFunc {
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
		notificationID := r.PathValue("notificationId")
		if !requirePathUUID(w, notificationID) {
			return
		}
		// The row is recipient-scoped already, but the caller must still hold
		// workspace.read on the workspace the notification belongs to — same
		// gate as the list surface.
		var workspaceID string
		err := deps.Store.Pool.QueryRow(r.Context(), `SELECT workspace_id::text FROM content.notifications WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND id = $3::uuid`, principal.OrganizationID, principal.UserID, notificationID).Scan(&workspaceID)
		if err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, http.StatusNotFound, "notification_not_found")
				return
			}
			writeError(w, http.StatusInternalServerError, "notification_read_failed")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "workspace.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		result, err := deps.Store.Pool.Exec(r.Context(), `UPDATE content.notifications SET read_at = COALESCE(read_at, now()) WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND id = $3::uuid`, principal.OrganizationID, principal.UserID, notificationID)
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

func markAllNotificationsRead(deps Dependencies) http.HandlerFunc {
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
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) || !rejectUnknownWorkspace(w, r, deps, principal) {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "workspace.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		if _, err := deps.Store.Pool.Exec(r.Context(), `UPDATE content.notifications SET read_at = COALESCE(read_at, now()) WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid AND read_at IS NULL`, principal.OrganizationID, workspaceID, principal.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "notifications_read_all_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func notificationStream(deps Dependencies) http.HandlerFunc {
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
		SELECT `+notificationColumns+`
		FROM content.notifications
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND recipient_user_id = $3::uuid AND stream_id > $4
		ORDER BY stream_id LIMIT 100
	`, organizationID, workspaceID, userID, lastID)
	if err != nil {
		return lastID, err
	}
	defer rows.Close()
	for rows.Next() {
		var row notificationRow
		if err := rows.Scan(&row.streamID, &row.id, &row.workspaceID, &row.kind, &row.payload, &row.readAt, &row.createdAt); err != nil {
			return lastID, err
		}
		payload, err := json.Marshal(presentNotification(row))
		if err != nil {
			return lastID, err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: notification\ndata: %s\n\n", row.streamID, payload); err != nil {
			return lastID, err
		}
		lastID = row.streamID
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
