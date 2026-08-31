package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/tag"
)

func writeMemberAssetError(w http.ResponseWriter, err error, fallback string) {
	var schemaErr *resourcemodel.SchemaValidationError
	switch {
	case errors.Is(err, assetservice.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, assetservice.ErrUnknownTagKey), errors.Is(err, tag.ErrUnknownTag):
		writeError(w, http.StatusUnprocessableEntity, "unknown_tag")
	case errors.Is(err, tag.ErrContradictoryFilter):
		writeError(w, http.StatusUnprocessableEntity, "contradictory_tag_filter")
	case errors.Is(err, assetservice.ErrTooManyTags), errors.Is(err, tag.ErrTooManyTags):
		writeError(w, http.StatusUnprocessableEntity, "too_many_tags")
	case errors.Is(err, assetservice.ErrTagArchived), errors.Is(err, tag.ErrArchived):
		writeError(w, http.StatusConflict, "tag_archived")
	case errors.As(err, &schemaErr):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "model_schema_invalid", "message": "resource model schema is invalid", "request_id": requestID(), "details": schemaErr})
	case errors.Is(err, assetservice.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, assetservice.ErrNotFound):
		writeError(w, http.StatusNotFound, "asset_not_found")
	case errors.Is(err, assetservice.ErrConflict):
		writeError(w, http.StatusConflict, "version_conflict")
	default:
		log.Printf("member asset request failed: %v", err)
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func listMemberAssets(deps Dependencies) http.HandlerFunc {
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
		filters, err := decodeAssetListFilters(query.Get("filters"))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		// Phase 2 tag semantics: tags_any/tags_all/tags_none replace the
		// retired top-level tags parameter and the retired tag jsonb bypass in
		// the filters document. Neither is honored, not even with a shim.
		if _, legacy := query["tags"]; legacy {
			writeError(w, http.StatusUnprocessableEntity, "legacy_tags_field_not_supported")
			return
		}
		if _, legacy := filters["tags"]; legacy {
			writeError(w, http.StatusUnprocessableEntity, "legacy_tags_field_not_supported")
			return
		}
		page, err := deps.MemberAssetService.ListPage(r.Context(), principal, r.PathValue("workspaceId"), assetservice.MemberAssetListInput{
			Query: query.Get("q"), ResourceModelID: query.Get("resource_model_id"), ContentKind: query.Get("content_kind"),
			Visibility: query.Get("visibility"), PublicationStatus: query.Get("publication_status"),
			CreatedBy: query.Get("created_by"), ContainerID: query.Get("container_id"), ParentAssetID: query.Get("parent_asset_id"),
			TagsAny: query["tags_any"], TagsAll: query["tags_all"], TagsNone: query["tags_none"],
			Sort: query.Get("sort"), Filters: filters, Limit: limit, Cursor: query.Get("cursor"),
		})
		if err != nil {
			writeMemberAssetError(w, err, "asset_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": page.Items, "has_more": page.HasMore, "next_cursor": page.NextCursor})
	}
}

func decodeAssetListFilters(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var filters map[string]any
	if err := decoder.Decode(&filters); err != nil || filters == nil {
		return nil, assetservice.ErrInvalidInput
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, assetservice.ErrInvalidInput
	}
	return filters, nil
}

func createMemberAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input assetservice.MemberAssetInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if !rejectBlankText(w, input.Title, input.Markdown) {
			return
		}
		if !requirePathUUID(w, r.PathValue("workspaceId")) || !rejectUnknownWorkspace(w, r, deps, principal) {
			return
		}
		result, err := deps.MemberAssetService.Create(r.Context(), principal, r.PathValue("workspaceId"), key, input)
		if err != nil {
			writeMemberAssetError(w, err, "asset_create_failed")
			return
		}
		writeETag(w, result.ETag)
		writeJSON(w, http.StatusCreated, result)
	}
}

func memberAssetsCollection(deps Dependencies) http.HandlerFunc {
	list := listMemberAssets(deps)
	create := createMemberAsset(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list(w, r)
			return
		}
		create(w, r)
	}
}

func getMemberAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		result, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
		if err != nil {
			writeMemberAssetError(w, err, "asset_load_failed")
			return
		}
		writeETag(w, result.ETag)
		writeJSON(w, http.StatusOK, result)
	}
}

func patchMemberAsset(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		if strings.TrimSpace(r.Header.Get("If-Match")) == "" {
			writeError(w, http.StatusUnprocessableEntity, "if_match_required")
			return
		}
		var input assetservice.MemberAssetPatch
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if !rejectBlankText(w, input.Title, input.Markdown) {
			return
		}
		if !requirePathUUID(w, r.PathValue("assetId")) {
			return
		}
		expected := strings.Trim(r.Header.Get("If-Match"), "\"")
		result, err := deps.MemberAssetService.Update(r.Context(), principal, r.PathValue("assetId"), expected, key, input)
		if err != nil {
			writeMemberAssetError(w, err, "asset_update_failed")
			return
		}
		writeETag(w, result.ETag)
		writeJSON(w, http.StatusOK, result)
	}
}

func memberAssetResource(deps Dependencies) http.HandlerFunc {
	get := getMemberAsset(deps)
	patch := patchMemberAsset(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		patch(w, r)
	}
}
