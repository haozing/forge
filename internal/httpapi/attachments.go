package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agentchunzhi/internal/attachment"
)

func writeAttachmentError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, attachment.ErrNotFound):
		writeError(w, http.StatusNotFound, "attachment_not_found")
	case errors.Is(err, attachment.ErrInvalidUpload):
		writeError(w, http.StatusUnprocessableEntity, "invalid_attachment")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func listAttachments(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.AttachmentService.List(r.Context(), principal, r.PathValue("versionId"))
		if err != nil {
			writeAttachmentError(w, err, "attachment_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func getAttachment(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.AttachmentService.Status(r.Context(), principal, r.PathValue("attachmentId"))
		if err != nil {
			writeAttachmentError(w, err, "attachment_status_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

type patchAttachmentRequest struct {
	Filename string `json:"filename"`
}

func patchAttachment(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
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
		var input patchAttachmentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Filename) == "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		item, err := deps.AttachmentService.UpdateFilename(r.Context(), principal, r.PathValue("attachmentId"), input.Filename)
		if err != nil {
			writeAttachmentError(w, err, "attachment_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func deleteAttachment(deps Dependencies) http.HandlerFunc {
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
		if err := deps.AttachmentService.Delete(r.Context(), principal, r.PathValue("attachmentId")); err != nil {
			writeAttachmentError(w, err, "attachment_delete_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type linkAttachmentRequest struct {
	AssetID string `json:"asset_id"`
	Role string `json:"role"` // body | cover (二期 §6)
}

func linkAttachment(deps Dependencies) http.HandlerFunc {
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
		var input linkAttachmentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.AssetID) == "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if err := deps.AttachmentService.Link(r.Context(), principal, r.PathValue("attachmentId"), input.AssetID, input.Role); err != nil {
			writeAttachmentError(w, err, "attachment_link_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func assetVersionAttachments(deps Dependencies) http.HandlerFunc {
	list := listAttachments(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			list(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

func attachmentResource(deps Dependencies) http.HandlerFunc {
	get := getAttachment(deps)
	patch := patchAttachment(deps)
	remove := deleteAttachment(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			get(w, r)
		case http.MethodPatch:
			patch(w, r)
		case http.MethodDelete:
			remove(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}
