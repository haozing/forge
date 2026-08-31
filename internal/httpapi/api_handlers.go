package httpapi

// api_handlers.go — the contract-surface vertical slice of phase 0: publication
// requests (review), asset draft autosave, commit and lifecycle commands.
// Handlers only authenticate, parse, call domain services and map errors.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/review"
	"agentchunzhi/internal/tag"
)

// requireIdempotencyKey enforces the mandatory Idempotency-Key contract on
// contract write commands: missing returns 428.
func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusPreconditionRequired, "idempotency_key_required")
		return "", false
	}
	if len(key) > 200 {
		writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
		return "", false
	}
	return key, true
}

func sessionPrincipal(w http.ResponseWriter, r *http.Request, deps Dependencies) (auth.Principal, bool) {
	principal, err := deps.SessionService.Authenticate(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required")
		return auth.Principal{}, false
	}
	if principal.UserType != auth.UserTypeMember {
		writeError(w, http.StatusForbidden, "member_session_required")
		return auth.Principal{}, false
	}
	return principal, true
}

// ServiceError maps domain errors onto the HTTP status/code contract.
func ServiceError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, asset.ErrNotFound), errors.Is(err, review.ErrNotFound):
		writeError(w, http.StatusNotFound, "resource_not_found")
	case errors.Is(err, asset.ErrInvalidInput), errors.Is(err, review.ErrInvalidInput),
		errors.Is(err, asset.ErrInvalidVisibility), errors.Is(err, asset.ErrInvalidOrigin),
		errors.Is(err, asset.ErrInvalidConfirmation):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, asset.ErrDraftRevisionMismatch):
		writeError(w, http.StatusPreconditionFailed, "draft_revision_mismatch")
	case errors.Is(err, asset.ErrAssetArchived), errors.Is(err, asset.ErrConflict),
		errors.Is(err, review.ErrConflict), errors.Is(err, review.ErrVersionSuperseded):
		writeError(w, http.StatusConflict, "state_conflict")
	case errors.Is(err, review.ErrSelfApproval):
		writeError(w, http.StatusConflict, "self_approval_not_allowed")
	case errors.Is(err, asset.ErrApprovalRequired), errors.Is(err, asset.ErrForbidden),
		errors.Is(err, review.ErrForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	case errors.Is(err, asset.ErrConfirmationRequired):
		writeError(w, http.StatusConflict, "human_confirmation_required")
	case errors.Is(err, asset.ErrAttachmentNotClean):
		writeError(w, http.StatusConflict, "attachments_not_clean")
	case errors.Is(err, asset.ErrRequiredFieldMissing):
		writeError(w, http.StatusConflict, "required_field_missing")
	case errors.Is(err, asset.ErrTagArchived):
		writeError(w, http.StatusConflict, "tag_archived")
	case errors.Is(err, asset.ErrTooManyTags):
		writeError(w, http.StatusUnprocessableEntity, "too_many_tags")
	case errors.Is(err, asset.ErrSuggestionNotFound):
		writeError(w, http.StatusNotFound, "suggestion_not_found")
	case errors.Is(err, asset.ErrSuggestionStateInvalid):
		writeError(w, http.StatusConflict, "suggestion_state_invalid")
	case errors.Is(err, asset.ErrSuggestionKindInvalid):
		writeError(w, http.StatusUnprocessableEntity, "suggestion_kind_invalid")
	case errors.Is(err, authz.ErrWorkspaceForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	case errors.Is(err, authz.ErrWorkspaceNotFound):
		writeError(w, http.StatusNotFound, "resource_not_found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
		return false
	}
	return true
}

// ---------- publication requests ----------

type SubmitRequest struct {
	AssetID       string `json:"asset_id"`
	DraftRevision string `json:"draft_revision"`
	Comment       string `json:"comment"`
}

func PublicationRequests(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			page, err := deps.ReviewService.ListPage(r.Context(), principal, workspaceID, review.ListInput{
				Status:      r.URL.Query().Get("status"),
				SubmittedBy: r.URL.Query().Get("submitted_by"),
				Limit:       atoiDefault(r.URL.Query().Get("limit"), 20),
				Cursor:      r.URL.Query().Get("cursor"),
			})
			if err != nil {
				ServiceError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{
				"items": page.Items,
				"page":  pageFrom(len(page.Items), atoiDefault(r.URL.Query().Get("limit"), 20), page.NextCursor),
			})
		case http.MethodPost:
			key, ok := requireIdempotencyKey(w, r)
			if !ok {
				return
			}
			var input SubmitRequest
			if !decodeBody(w, r, &input, 64*1024) {
				return
			}
			request, err := deps.ReviewService.Submit(r.Context(), principal, workspaceID, input.AssetID, input.DraftRevision, key, input.Comment)
			if err != nil {
				ServiceError(w, err)
				return
			}
			writeData(w, r, http.StatusCreated, request)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

func PublicationRequest(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		request, err := deps.ReviewService.Get(r.Context(), principal, r.PathValue("workspaceId"), r.PathValue("requestId"))
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeETag(w, request.ETag)
		writeData(w, r, http.StatusOK, request)
	}
}

type DecisionBody struct {
	Comment string `json:"comment"`
}

func PublicationDecide(deps Dependencies, decision string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		var body DecisionBody
		if r.ContentLength != 0 {
			if !decodeBody(w, r, &body, 64*1024) {
				return
			}
		}
		var (
			request review.Request
			err     error
		)
		if decision == "approve" {
			request, err = deps.ReviewService.Approve(r.Context(), principal, r.PathValue("workspaceId"), r.PathValue("requestId"), body.Comment)
		} else {
			request, err = deps.ReviewService.Reject(r.Context(), principal, r.PathValue("workspaceId"), r.PathValue("requestId"), body.Comment)
		}
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeETag(w, request.ETag)
		writeData(w, r, http.StatusOK, request)
	}
}

type CancelBody struct {
	Reason string `json:"reason"`
}

func PublicationCancel(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		var body CancelBody
		if r.ContentLength != 0 {
			if !decodeBody(w, r, &body, 16*1024) {
				return
			}
		}
		request, err := deps.ReviewService.Cancel(r.Context(), principal, r.PathValue("workspaceId"), r.PathValue("requestId"), body.Reason)
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeETag(w, request.ETag)
		writeData(w, r, http.StatusOK, request)
	}
}

type BatchBody struct {
	Decision string             `json:"decision"`
	Items    []review.BatchItem `json:"items"`
}

func PublicationBatch(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		var body BatchBody
		if !decodeBody(w, r, &body, 256*1024) {
			return
		}
		if len(body.Items) == 0 || len(body.Items) > 100 {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.ReviewService.Batch(r.Context(), principal, r.PathValue("workspaceId"), body.Decision, body.Items)
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, result)
	}
}

func PublicationComments(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		requestID := r.PathValue("requestId")
		switch r.Method {
		case http.MethodGet:
			comments, _, err := deps.ReviewService.ListComments(r.Context(), principal, workspaceID, requestID, "", 100)
			if err != nil {
				ServiceError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{"items": comments, "page": CursorPage{NextCursor: nil, HasMore: false}})
		case http.MethodPost:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var body struct {
				Body string `json:"body"`
			}
			if !decodeBody(w, r, &body, 64*1024) {
				return
			}
			comment, err := deps.ReviewService.AddComment(r.Context(), principal, workspaceID, requestID, body.Body)
			if err != nil {
				ServiceError(w, err)
				return
			}
			writeData(w, r, http.StatusCreated, comment)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// ---------- asset draft + lifecycle ----------

// AssetDraftPatchBody extends the draft autosave patch with the phase 2
// tag_ids replacement set. Omitting tag_ids keeps the current tags; passing a
// list (possibly empty) reconciles the draft tag set.
type AssetDraftPatchBody struct {
	asset.DraftPatch
	TagIDs *[]string `json:"tag_ids"`
}

// AssetDraftResponse is the draft representation with its tag summaries
// attached; the contract never returns bare tag id strings.
type AssetDraftResponse struct {
	asset.Draft
	Tags []tag.Summary `json:"tags"`
}

func AssetDraft(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		assetID := r.PathValue("assetId")
		if !requirePathUUID(w, assetID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			draft, err := deps.MemberAssetService.GetDraft(r.Context(), principal, assetID)
			if err != nil {
				ServiceError(w, err)
				return
			}
			tags, err := deps.MemberAssetService.DraftTags(r.Context(), principal, assetID)
			if err != nil {
				ServiceError(w, err)
				return
			}
			writeETag(w, `"`+itoa(draft.Revision)+`"`)
			writeData(w, r, http.StatusOK, AssetDraftResponse{Draft: draft, Tags: tags})
		case http.MethodPatch:
			// The draft revision contract: a missing If-Match is 428, a stale
			// revision is 412 (enforced by the draft service).
			expected, ok := requireIfMatch(w, r)
			if !ok {
				return
			}
			expected = expectedRevisionFromIfMatch(r)
			var body AssetDraftPatchBody
			if !decodeBody(w, r, &body, 4<<20) {
				return
			}
			patch := body.DraftPatch
			hasFields := patch.Title != nil || patch.Summary != nil || patch.Markdown != nil ||
				patch.Fields != nil || patch.Visibility != nil
			var (
				draft     asset.Draft
				summaries []tag.Summary
				err       error
			)
			if body.TagIDs == nil {
				// Content-only (or empty) patch: unchanged phase 0 behavior.
				draft, err = deps.MemberAssetService.AutosaveDraft(r.Context(), principal, assetID, expected, patch)
				if err != nil {
					ServiceError(w, err)
					return
				}
			} else {
				// Tag reconciliation runs after the content autosave. Both
				// commands own one revision bump each; SetDraftTags re-reads
				// the draft inside its own transaction, so once the autosave
				// has already advanced the revision we must not replay the
				// stale If-Match token (it would fail against the incremented
				// revision) — hence expectedRevision="" for the mixed case.
				if hasFields {
					if _, err = deps.MemberAssetService.AutosaveDraft(r.Context(), principal, assetID, expected, patch); err != nil {
						ServiceError(w, err)
						return
					}
					expected = ""
				}
				target, targetErr := deps.MemberAssetService.Get(r.Context(), principal, assetID)
				if targetErr != nil {
					ServiceError(w, targetErr)
					return
				}
				entries := make([]asset.DraftTagEntry, 0, len(*body.TagIDs))
				for _, id := range *body.TagIDs {
					entries = append(entries, asset.DraftTagEntry{TagID: id})
				}
				draft, summaries, err = deps.MemberAssetService.SetDraftTags(r.Context(), principal, target.WorkspaceID, assetID, expected, entries)
				if err != nil {
					ServiceError(w, err)
					return
				}
			}
			if summaries == nil {
				// An untouched tag set still reports its current summaries.
				summaries, err = deps.MemberAssetService.DraftTags(r.Context(), principal, assetID)
				if err != nil {
					ServiceError(w, err)
					return
				}
			}
			writeETag(w, `"`+itoa(draft.Revision)+`"`)
			writeData(w, r, http.StatusOK, AssetDraftResponse{Draft: draft, Tags: summaries})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

type CommitBody struct {
	// draft_revision is an integer in the contract (openapi.yaml).
	DraftRevision *int64 `json:"draft_revision"`
}

func CommitDraft(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		var body CommitBody
		if !decodeBody(w, r, &body, 16*1024) {
			return
		}
		if body.DraftRevision == nil {
			writeError(w, http.StatusUnprocessableEntity, "draft_revision_required")
			return
		}
		assetID := r.PathValue("assetId")
		target, err := deps.MemberAssetService.Get(r.Context(), principal, assetID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		result, err := deps.MemberAssetService.CommitDraft(r.Context(), principal, target.WorkspaceID, assetID,
			strconv.FormatInt(*body.DraftRevision, 10))
		if err != nil {
			ServiceError(w, err)
			return
		}
		version, err := deps.MemberAssetService.GetVersion(r.Context(), principal, result.VersionID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeData(w, r, http.StatusCreated, version)
	}
}

func memberPublishAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		var body struct {
			DraftRevision string `json:"draft_revision"`
		}
		if r.ContentLength != 0 {
			if !decodeBody(w, r, &body, 16*1024) {
				return
			}
		}
		assetID := r.PathValue("assetId")
		target, err := deps.MemberAssetService.Get(r.Context(), principal, assetID)
		if err != nil {
			ServiceError(w, err)
			return
		}
		result, err := deps.MemberAssetService.Publish(r.Context(), principal, target.WorkspaceID, assetID, body.DraftRevision, requireIdempotencyKeyValue(r))
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeETag(w, result.ETag)
		writeData(w, r, http.StatusOK, result)
	}
}

func requireIdempotencyKeyValue(r *http.Request) string {
	return r.Header.Get("Idempotency-Key")
}

func memberArchiveAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		assetID := r.PathValue("assetId")
		if _, err := deps.MemberAssetService.Get(r.Context(), principal, assetID); err != nil {
			ServiceError(w, err)
			return
		}
		result, err := deps.MemberAssetService.Archive(r.Context(), principal, assetID, requireIdempotencyKeyValue(r))
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeETag(w, result.ETag)
		writeData(w, r, http.StatusOK, result)
	}
}

func memberRestoreAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		assetID := r.PathValue("assetId")
		if _, err := deps.MemberAssetService.Get(r.Context(), principal, assetID); err != nil {
			ServiceError(w, err)
			return
		}
		result, err := deps.MemberAssetService.Restore(r.Context(), principal, assetID, requireIdempotencyKeyValue(r))
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeETag(w, result.ETag)
		writeData(w, r, http.StatusOK, result)
	}
}

func ConfirmVersion(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		version, err := deps.MemberAssetService.ConfirmVersion(r.Context(), principal, r.PathValue("versionId"), requireIdempotencyKeyValue(r))
		if err != nil {
			ServiceError(w, err)
			return
		}
		writeData(w, r, http.StatusCreated, version)
	}
}

// atoiDefaultLimit is the upper bound applied to client-supplied page sizes;
// it matches the largest per-surface limit the query handlers accept.
const atoiDefaultLimit = 200

// atoiDefault parses a decimal limit, falling back to the default for absent,
// non-numeric or non-positive input. Overflowing and oversized values are
// clamped instead of wrapping (a hand-rolled digit loop would silently
// overflow int and feed an arbitrary page size downstream).
func atoiDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	if parsed > atoiDefaultLimit {
		return atoiDefaultLimit
	}
	return parsed
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	negative := value < 0
	if negative {
		value = -value
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// expectedRevisionFromIfMatch extracts the draft revision from an If-Match
// header value of the form "<revision>" (ETag-quoted or bare digits).
func expectedRevisionFromIfMatch(r *http.Request) string {
	value := r.Header.Get("If-Match")
	if value == "" {
		return ""
	}
	for len(value) > 0 && (value[0] == '"' || value[len(value)-1] == '"') {
		value = strings.Trim(value, "\"")
	}
	return value
}
