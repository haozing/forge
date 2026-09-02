package httpapi

import (
	"encoding/json"
	"errors"
	"io"
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
	case errors.Is(err, attachment.ErrUploadTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "attachment_too_large")
	case errors.Is(err, attachment.ErrForbidden):
		writeError(w, http.StatusForbidden, "attachment_forbidden")
	case errors.Is(err, attachment.ErrAssetArchived):
		writeError(w, http.StatusConflict, "asset_archived")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

// uploadAttachment is the member-facing multipart upload endpoint (doc §11.1
// "附件上传和下载"): one file part named "file" becomes a standalone scanning
// attachment; callers bind it to an asset draft through the link endpoint.
func uploadAttachment(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID) {
			return
		}
		maxBytes := deps.AttachmentService.MaxBytes
		if maxBytes <= 0 {
			maxBytes = 50 * 1024 * 1024
		}
		// The multipart envelope adds a little overhead over the raw object.
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_multipart_body")
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		files := r.MultipartForm.File["file"]
		if len(files) != 1 {
			writeError(w, http.StatusUnprocessableEntity, "file_part_required")
			return
		}
		header := files[0]
		if header.Size <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_attachment")
			return
		}
		file, err := header.Open()
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_attachment")
			return
		}
		defer file.Close()
		seeker, ok := file.(interface {
			io.ReadSeeker
		})
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_attachment")
			return
		}
		item, err := deps.AttachmentService.Upload(r.Context(), principal, workspaceID,
			header.Filename, header.Header.Get("Content-Type"), header.Size, seeker)
		if err != nil {
			writeAttachmentError(w, err, "attachment_upload_failed")
			return
		}
		writeJSON(w, http.StatusCreated, item)
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
