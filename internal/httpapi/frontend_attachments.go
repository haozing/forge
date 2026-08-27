package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agentchunzhi/internal/attachment"
)

func writeFrontendAttachmentError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, attachment.ErrNotFound):
		writeError(w, http.StatusNotFound, "attachment_not_found")
	case errors.Is(err, attachment.ErrInvalidUpload):
		writeError(w, http.StatusUnprocessableEntity, "invalid_attachment")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func listFrontendAttachments(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		allowed, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.read")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		items, err := deps.AttachmentService.List(r.Context(), principal, r.PathValue("versionId"), allowed)
		if err != nil {
			writeFrontendAttachmentError(w, err, "attachment_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func getFrontendAttachment(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		allowed, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.read")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		item, err := deps.AttachmentService.Status(r.Context(), principal, r.PathValue("attachmentId"), allowed)
		if err != nil {
			writeFrontendAttachmentError(w, err, "attachment_status_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

type patchFrontendAttachmentRequest struct {
	Filename string `json:"filename"`
}

func patchFrontendAttachment(deps Dependencies) http.HandlerFunc {
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
		var input patchFrontendAttachmentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Filename) == "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		allowed, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.write")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		item, err := deps.AttachmentService.UpdateFilename(r.Context(), principal, r.PathValue("attachmentId"), input.Filename, allowed)
		if err != nil {
			writeFrontendAttachmentError(w, err, "attachment_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func deleteFrontendAttachment(deps Dependencies) http.HandlerFunc {
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
		allowed, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.write")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		if err := deps.AttachmentService.Delete(r.Context(), principal, r.PathValue("attachmentId"), allowed); err != nil {
			writeFrontendAttachmentError(w, err, "attachment_delete_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type linkFrontendAttachmentRequest struct {
	AssetVersionID string `json:"asset_version_id"`
}

func linkFrontendAttachment(deps Dependencies) http.HandlerFunc {
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
		var input linkFrontendAttachmentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.AssetVersionID) == "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		allowed, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.write")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		if err := deps.AttachmentService.Link(r.Context(), principal, attachment.LinkInput{
			AttachmentID:   r.PathValue("attachmentId"),
			AssetVersionID: input.AssetVersionID,
		}, allowed); err != nil {
			writeFrontendAttachmentError(w, err, "attachment_link_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func frontendAssetVersionAttachments(deps Dependencies) http.HandlerFunc {
	upload := uploadAttachment(deps)
	list := listFrontendAttachments(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			list(w, r)
		case http.MethodPost:
			upload(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

func frontendAttachmentResource(deps Dependencies) http.HandlerFunc {
	get := getFrontendAttachment(deps)
	patch := patchFrontendAttachment(deps)
	remove := deleteFrontendAttachment(deps)
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
