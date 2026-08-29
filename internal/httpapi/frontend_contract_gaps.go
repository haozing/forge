package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentchunzhi/internal/automation"
	"agentchunzhi/internal/content"
	agentquery "agentchunzhi/internal/query"
	"github.com/jackc/pgx/v5"
)



func searchSuggestions(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" || len(q) > 200 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_query")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if deps.WorkspacePolicy == nil || deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusInternalServerError, "authorization_unconfigured")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "workspace.read"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		modelID := strings.TrimSpace(r.URL.Query().Get("resource_model_id"))
		if modelID != "" && !agentquery.ValidUUID(modelID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_resource_model_id")
			return
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			WITH source AS (
				SELECT v.title AS value, 'title'::text AS kind, count(*)::bigint AS item_count
				FROM asset.assets a
				JOIN asset.asset_versions v ON v.id = a.current_working_version_id
				WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
                                  AND ($3 = '' OR a.resource_model_id = NULLIF($3, '')::uuid)
				  AND v.title IS NOT NULL AND v.title ILIKE '%' || $4 || '%'
				GROUP BY v.title
				UNION ALL
				SELECT tag.value, 'tag'::text, count(*)::bigint
				FROM asset.assets a
				JOIN asset.asset_versions v ON v.id = a.current_working_version_id
				CROSS JOIN LATERAL jsonb_array_elements_text(
					CASE WHEN jsonb_typeof(v.tags) = 'array' THEN v.tags ELSE '[]'::jsonb END
				) tag(value)
				WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
                                  AND ($3 = '' OR a.resource_model_id = NULLIF($3, '')::uuid)
				  AND tag.value ILIKE '%' || $4 || '%'
				GROUP BY tag.value
			)
			SELECT value, kind, item_count FROM source ORDER BY item_count DESC, kind, value LIMIT 20`,
			principal.OrganizationID, workspaceID, modelID, q)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "search_suggestions_failed")
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var value, kind string
			var count int64
			if err := rows.Scan(&value, &kind, &count); err != nil {
				writeError(w, http.StatusInternalServerError, "search_suggestions_failed")
				return
			}
			items = append(items, map[string]any{"value": value, "label": value, "kind": kind, "count": count})
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "search_suggestions_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

type conversationPatchRequest struct {
	Title      *string `json:"title"`
	Visibility *string `json:"visibility"`
}

func conversationResourceFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if r.Method == http.MethodGet {
			getConversation(deps)(w, r)
			return
		}
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusInternalServerError, "database_unconfigured")
			return
		}
		if _, err := deps.ConversationService.GetConversation(r.Context(), principal, r.PathValue("conversationId")); err != nil {
			if errors.Is(err, content.ErrNotFound) {
				writeError(w, http.StatusNotFound, "conversation_not_found")
			} else {
				writeError(w, http.StatusInternalServerError, "conversation_load_failed")
			}
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input conversationPatchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || (input.Title == nil && input.Visibility == nil) {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if input.Visibility != nil && *input.Visibility != "private" && *input.Visibility != "workspace" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_visibility")
			return
		}
		result, err := deps.Store.Pool.Exec(r.Context(), `
			UPDATE content.conversations c
			SET title = COALESCE($3, c.title), visibility = COALESCE($4, c.visibility), updated_at = now()
			WHERE c.organization_id = $1::uuid AND c.id = $2::uuid
			  AND EXISTS (SELECT 1 FROM content.workspace_members wm WHERE wm.organization_id = c.organization_id AND wm.workspace_id = c.workspace_id AND wm.user_id = $5::uuid)`,
			principal.OrganizationID, r.PathValue("conversationId"), input.Title, input.Visibility, principal.UserID)
		if err != nil || result.RowsAffected() != 1 {
			writeError(w, http.StatusInternalServerError, "conversation_update_failed")
			return
		}
		item, err := deps.ConversationService.GetConversation(r.Context(), principal, r.PathValue("conversationId"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func archiveConversationFinal(deps Dependencies) http.HandlerFunc {
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
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusInternalServerError, "database_unconfigured")
			return
		}
		if _, err := deps.ConversationService.GetConversation(r.Context(), principal, r.PathValue("conversationId")); err != nil {
			if errors.Is(err, content.ErrNotFound) {
				writeError(w, http.StatusNotFound, "conversation_not_found")
			} else {
				writeError(w, http.StatusInternalServerError, "conversation_load_failed")
			}
			return
		}
		if _, err := deps.Store.Pool.Exec(r.Context(), `UPDATE content.conversations SET status = 'archived', completed_at = COALESCE(completed_at, now()), updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, r.PathValue("conversationId")); err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_archive_failed")
			return
		}
		item, err := deps.ConversationService.GetConversation(r.Context(), principal, r.PathValue("conversationId"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func conversationBlocksFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.ConversationService.ListMessages(r.Context(), principal, r.PathValue("conversationId"))
		if errors.Is(err, content.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation_not_found")
			return
		}
		if errors.Is(err, content.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_id")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_blocks_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "blocks": items, "has_more": false})
	}
}

func conversationNoteFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusInternalServerError, "database_unconfigured")
			return
		}
		var noteAssetID, noteContainerID, versionID string
		var title, markdown *string
		var fields []byte
		var publicationStatus, quality string
		var messageCount int64
		err := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT nb.note_asset_id::text, nb.note_container_id::text,
			       COALESCE(a.current_working_version_id::text, ''), v.title, v.markdown, v.fields,
			       a.publication_status, COALESCE(v.quality, ''), count(mb.block_revision_id)
			FROM content.note_bindings nb
                        JOIN content.conversations c ON c.id = nb.conversation_id
                        JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
			JOIN asset.assets a ON a.organization_id = c.organization_id AND a.id = nb.note_asset_id
			LEFT JOIN asset.asset_versions v ON v.id = a.current_working_version_id
			LEFT JOIN content.message_blocks mb ON mb.organization_id = c.organization_id AND mb.conversation_id = c.id
                        WHERE nb.organization_id = $1::uuid AND nb.conversation_id = $2::uuid
                          AND (c.visibility = 'workspace' OR c.initiator_user_id = $3::uuid)
			GROUP BY nb.note_asset_id, nb.note_container_id, a.current_working_version_id, v.title, v.markdown, v.fields, a.publication_status, v.quality`,
			principal.OrganizationID, r.PathValue("conversationId"), principal.UserID).Scan(&noteAssetID, &noteContainerID, &versionID, &title, &markdown, &fields, &publicationStatus, &quality, &messageCount)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "conversation_or_note_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_note_load_failed")
			return
		}
		decodedFields := map[string]any{}
		if len(fields) > 0 {
			_ = json.Unmarshal(fields, &decodedFields)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"conversation_id": r.PathValue("conversationId"), "note_asset_id": noteAssetID, "note_container_id": noteContainerID,
			"asset_version_id": versionID, "title": title, "markdown": markdown, "fields": decodedFields,
			"publication_status": publicationStatus, "quality": quality, "message_count": messageCount,
		})
	}
}

func conversationTranscriptFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		media, err := deps.ConversationService.GetMedia(r.Context(), principal, r.PathValue("mediaId"))
		if errors.Is(err, content.ErrNotFound) {
			writeError(w, http.StatusNotFound, "media_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "media_load_failed")
			return
		}
		result := map[string]any{"media_id": media.MediaID, "status": media.Status, "block_revision_id": media.TranscriptionBlockRevisionID, "text": nil, "content": nil}
		if media.TranscriptionBlockRevisionID != "" && deps.Store != nil && deps.Store.Pool != nil {
			var text, format string
			var props []byte
			var createdAt time.Time
			err := deps.Store.Pool.QueryRow(r.Context(), `SELECT content, content_format, props, created_at FROM content.block_revisions WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, media.TranscriptionBlockRevisionID).Scan(&text, &format, &props, &createdAt)
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "transcript_not_found")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "transcript_load_failed")
				return
			}
			var decodedProps map[string]any
			_ = json.Unmarshal(props, &decodedProps)
			result["text"] = text
			result["content"] = text
			result["content_format"] = format
			result["props"] = decodedProps
			result["created_at"] = createdAt
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func deleteAutomationJobFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
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
		if !requirePathUUID(w, r.PathValue("jobId")) {
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusInternalServerError, "database_unconfigured")
			return
		}
		job, err := deps.AutomationService.GetJob(r.Context(), principal, r.PathValue("jobId"))
		if errors.Is(err, automation.ErrNotFound) {
			writeError(w, http.StatusNotFound, "automation_not_found")
			return
		}
		if err != nil {
			writeAutomationError(w, err, "automation_job_load_failed")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, job.WorkspaceID, "", "automation.write"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		result, err := deps.Store.Pool.Exec(r.Context(), `DELETE FROM automation.jobs WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, job.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "automation_job_delete_failed")
			return
		}
		if result.RowsAffected() != 1 {
			writeError(w, http.StatusNotFound, "automation_not_found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type auditCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func auditLogsFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil || deps.WorkspacePolicy == nil {
			writeError(w, http.StatusInternalServerError, "authorization_unconfigured")
			return
		}
		scope, err := deps.WorkspacePolicy.Require(r.Context(), principal, r.PathValue("workspaceId"), "", "audit.read")
		if err != nil {
			writeError(w, http.StatusForbidden, "audit_access_denied")
			return
		}
		if scope.Role != "owner" && scope.Role != "admin" {
			writeError(w, http.StatusForbidden, "audit_access_denied")
			return
		}
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				writeError(w, http.StatusUnprocessableEntity, "invalid_limit")
				return
			}
			limit = parsed
		}
		from, to := strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to"))
		if from != "" {
			if _, err := time.Parse(time.RFC3339, from); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid_from")
				return
			}
		}
		if to != "" {
			if _, err := time.Parse(time.RFC3339, to); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid_to")
				return
			}
		}
		object := strings.TrimSpace(r.URL.Query().Get("object"))
		objectType, objectID := strings.TrimSpace(r.URL.Query().Get("object_type")), strings.TrimSpace(r.URL.Query().Get("object_id"))
		if object != "" {
			if agentquery.ValidUUID(object) {
				objectID = object
			} else {
				objectType = object
			}
		}
		if actor := strings.TrimSpace(r.URL.Query().Get("actor")); actor != "" && !agentquery.ValidUUID(actor) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_actor")
			return
		}
		if objectID != "" && !agentquery.ValidUUID(objectID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_object_id")
			return
		}
		cursorTime, cursorID := "", ""
		if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
			decoded, decodeErr := base64.RawURLEncoding.DecodeString(raw)
			var cursor auditCursor
			if decodeErr != nil || json.Unmarshal(decoded, &cursor) != nil || !agentquery.ValidUUID(cursor.ID) {
				writeError(w, http.StatusUnprocessableEntity, "invalid_cursor")
				return
			}
			cursorTime, cursorID = cursor.CreatedAt, cursor.ID
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `
			SELECT al.id::text, COALESCE(au.id::text, ''), COALESCE(au.display_name, ''),
			       COALESCE(iu.id::text, ''), COALESCE(iu.display_name, ''), al.action,
			       COALESCE(al.resource_type, ''), COALESCE(al.resource_id::text, ''), al.result,
			       al.metadata, al.created_at
			FROM audit.audit_log al
			LEFT JOIN identity.users au ON au.id = al.actor_user_id
			LEFT JOIN identity.users iu ON iu.id = al.initiator_user_id
			WHERE al.organization_id = $1::uuid
			  AND (al.metadata->>'workspace_id' = $2::text OR (al.resource_type = 'asset' AND EXISTS (SELECT 1 FROM asset.assets a WHERE a.organization_id = al.organization_id AND a.id = al.resource_id AND a.workspace_id = $2::uuid)))
			  AND ($3 = '' OR al.action = $3)
			  AND ($4 = '' OR al.actor_user_id = NULLIF($4, '')::uuid)
			  AND ($5 = '' OR al.resource_type = $5)
			  AND ($6 = '' OR al.resource_id = NULLIF($6, '')::uuid)
			  AND ($7 = '' OR al.created_at >= NULLIF($7, '')::timestamptz)
			  AND ($8 = '' OR al.created_at <= NULLIF($8, '')::timestamptz)
			  AND ($9 = '' OR (al.created_at, al.id) < (NULLIF($9, '')::timestamptz, NULLIF($10, '')::uuid))
			ORDER BY al.created_at DESC, al.id DESC LIMIT $11`,
			principal.OrganizationID, r.PathValue("workspaceId"), r.URL.Query().Get("action"), r.URL.Query().Get("actor"), objectType, objectID, from, to, cursorTime, cursorID, limit+1)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "audit_log_list_failed")
			return
		}
		defer rows.Close()
		type auditItem struct {
			ID           string            `json:"id"`
			Actor        map[string]string `json:"actor"`
			Initiator    map[string]string `json:"initiator"`
			Action       string            `json:"action"`
			ResourceType string            `json:"resource_type"`
			ResourceID   string            `json:"resource_id,omitempty"`
			Result       string            `json:"result"`
			Metadata     map[string]any    `json:"metadata"`
			CreatedAt    time.Time         `json:"created_at"`
		}
		items := make([]auditItem, 0, limit+1)
		for rows.Next() {
			var item auditItem
			var actorID, actorName, initiatorID, initiatorName string
			var metadata []byte
			if err := rows.Scan(&item.ID, &actorID, &actorName, &initiatorID, &initiatorName, &item.Action, &item.ResourceType, &item.ResourceID, &item.Result, &metadata, &item.CreatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "audit_log_list_failed")
				return
			}
			item.Actor = map[string]string{"id": actorID, "display_name": actorName}
			item.Initiator = map[string]string{"id": initiatorID, "display_name": initiatorName}
			item.Metadata = map[string]any{}
			_ = json.Unmarshal(metadata, &item.Metadata)
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "audit_log_list_failed")
			return
		}
		hasMore := len(items) > limit
		if hasMore {
			items = items[:limit]
		}
		response := map[string]any{"items": items, "has_more": hasMore}
		if hasMore {
			last := items[len(items)-1]
			raw, _ := json.Marshal(auditCursor{CreatedAt: last.CreatedAt.UTC().Format(time.RFC3339Nano), ID: last.ID})
			response["next_cursor"] = base64.RawURLEncoding.EncodeToString(raw)
		}
		writeJSON(w, http.StatusOK, response)
	}
}
