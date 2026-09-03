package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"agentchunzhi/internal/content"
	"agentchunzhi/internal/conversation"
	"github.com/jackc/pgx/v5"
)

type conversationPatchRequest struct {
	Title      *string `json:"title"`
	Visibility *string `json:"visibility"`
}

func conversationResource(deps Dependencies) http.HandlerFunc {
	get := getConversation(deps)
	remove := deleteConversation(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			remove(w, r)
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
		if input.Visibility != nil && *input.Visibility != "workspace" && *input.Visibility != "organization" && *input.Visibility != "public" {
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
		writeData(w, r, http.StatusOK, item)
	}
}

func archiveConversation(deps Dependencies) http.HandlerFunc {
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
		writeData(w, r, http.StatusOK, item)
	}
}

// deleteConversation removes a thought. Derived thoughts block the delete
// with 409 unless ?cascade_children=true opts into subtree deletion.
func deleteConversation(deps Dependencies) http.HandlerFunc {
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
			return
		}
		cascade := r.URL.Query().Get("cascade_children") == "true"
		result, err := deps.ConversationService.Delete(r.Context(), principal, r.PathValue("conversationId"), cascade)
		if errors.Is(err, conversation.ErrInvalidID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_id")
			return
		}
		if errors.Is(err, conversation.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation_not_found")
			return
		}
		if errors.Is(err, conversation.ErrForbidden) {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		if errors.Is(err, conversation.ErrHasChildren) {
			writeError(w, http.StatusConflict, "conversation_has_children")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_delete_failed")
			return
		}
		writeData(w, r, http.StatusOK, result)
	}
}

func conversationBlocks(deps Dependencies) http.HandlerFunc {
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
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "blocks": items, "has_more": false})
	}
}

func conversationNote(deps Dependencies) http.HandlerFunc {
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
		var publicationStatus, confirmationStatus string
		var messageCount int64
		err := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT nb.note_asset_id::text, nb.note_container_id::text,
			       COALESCE(a.current_working_version_id::text, ''), v.title, v.markdown, v.fields,
			       a.publication_status, COALESCE(v.confirmation_status, ''), count(mb.block_revision_id)
			FROM content.note_bindings nb
                        JOIN content.conversations c ON c.id = nb.conversation_id
                        JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
			JOIN asset.assets a ON a.organization_id = c.organization_id AND a.id = nb.note_asset_id
			LEFT JOIN asset.asset_versions v ON v.id = a.current_working_version_id
			LEFT JOIN content.message_blocks mb ON mb.organization_id = c.organization_id AND mb.conversation_id = c.id
                        WHERE nb.organization_id = $1::uuid AND nb.conversation_id = $2::uuid
			GROUP BY nb.note_asset_id, nb.note_container_id, a.current_working_version_id, v.title, v.markdown, v.fields, a.publication_status, v.confirmation_status`,
			principal.OrganizationID, r.PathValue("conversationId"), principal.UserID).Scan(&noteAssetID, &noteContainerID, &versionID, &title, &markdown, &fields, &publicationStatus, &confirmationStatus, &messageCount)
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
		writeData(w, r, http.StatusOK, map[string]any{
			"conversation_id": r.PathValue("conversationId"), "note_asset_id": noteAssetID, "note_container_id": noteContainerID,
			"asset_version_id": versionID, "title": title, "markdown": markdown, "fields": decodedFields,
			"publication_status": publicationStatus, "confirmation_status": confirmationStatus, "message_count": messageCount,
		})
	}
}

func conversationTranscript(deps Dependencies) http.HandlerFunc {
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
		writeData(w, r, http.StatusOK, result)
	}
}
