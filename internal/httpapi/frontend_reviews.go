package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"agentchunzhi/internal/review"
)

func writeReviewError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, review.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, review.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, review.ErrNotFound):
		writeError(w, http.StatusNotFound, "review_not_found")
	case errors.Is(err, review.ErrConflict):
		writeError(w, http.StatusConflict, "invalid_state_transition")
	default:
		log.Printf("review request failed: %v", err)
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func listReviews(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		query := r.URL.Query()
		limit := 0
		if rawLimit := query.Get("limit"); rawLimit != "" {
			var err error
			limit, err = strconv.Atoi(rawLimit)
			if err != nil || limit < 1 || limit > 100 {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
		}
		page, err := deps.ReviewService.ListPage(r.Context(), principal, r.PathValue("workspaceId"), review.ListInput{
			Status: query.Get("status"), ResourceModelID: query.Get("resource_model_id"), SubmittedBy: query.Get("submitted_by"),
			CreatedFrom: query.Get("created_from"), CreatedTo: query.Get("created_to"), Limit: limit, Cursor: query.Get("cursor"),
		})
		if err != nil {
			writeReviewError(w, err, "review_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": page.Items, "has_more": page.HasMore, "next_cursor": page.NextCursor})
	}
}

func getReview(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.ReviewService.Get(r.Context(), principal, r.PathValue("reviewId"))
		if err != nil {
			writeReviewError(w, err, "review_load_failed")
			return
		}
		writeETag(w, item.ETag)
		writeJSON(w, http.StatusOK, item)
	}
}

func decideReview(deps Dependencies, decision string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requiredIdempotencyKey(r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input review.DecisionInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if r.ContentLength != 0 {
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
		}
		result, err := deps.ReviewService.Decide(r.Context(), principal, r.PathValue("reviewId"), key, decision, input)
		if err != nil {
			writeReviewError(w, err, "review_decision_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

type reviewBatchRequest struct {
	Items []review.BatchItem `json:"items"`
}

func batchReviews(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requiredIdempotencyKey(r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input reviewBatchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || len(input.Items) == 0 || len(input.Items) > 100 {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		writeJSON(w, http.StatusMultiStatus, map[string]any{"items": deps.ReviewService.Batch(r.Context(), principal, key, input.Items)})
	}
}
