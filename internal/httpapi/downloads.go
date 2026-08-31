package httpapi

import (
	"net/http"
	"time"
)

func presignedAttachmentDownload(deps Dependencies) http.HandlerFunc {
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
		id := r.PathValue("attachmentId")
		if _, err := deps.AttachmentService.Status(r.Context(), principal, id); err != nil {
			writeError(w, http.StatusNotFound, "attachment_not_found")
			return
		}
		expires := time.Now().UTC().Add(10 * time.Minute)
		writeJSON(w, http.StatusOK, map[string]string{"download_url": "/api/attachments/" + id + "/download", "expires_at": expires.Format(time.RFC3339)})
	}
}
