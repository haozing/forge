package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/deletion"
)

func assetResourceFinal(deps Dependencies) http.HandlerFunc {
	get := getMemberAsset(deps)
	patch := patchMemberAsset(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		if r.Method == http.MethodPatch {
			patch(w, r)
			return
		}
		if r.Method != http.MethodDelete {
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
		item, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
		if err != nil {
			writeMemberAssetError(w, err, "asset_load_failed")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, item.WorkspaceID, item.ResourceModelID, "asset.archive"); err != nil {
			writeError(w, http.StatusForbidden, "permission_denied")
			return
		}
		job, err := (deletion.Service{Store: deps.Store}).Enqueue(r.Context(), principal, item.WorkspaceID, "asset", item.ID, key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "asset_delete_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	}
}

func assetVersionCollectionFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		assetID := r.PathValue("assetId")
		switch r.Method {
		case http.MethodGet:
			items, err := deps.MemberAssetService.ListVersions(r.Context(), principal, assetID)
			if err != nil {
				writeMemberAssetError(w, err, "asset_versions_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
		case http.MethodPost:
			key, ok := requestIdempotencyKey(w, r)
			if !ok {
				writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
				return
			}
			var input assetservice.MemberAssetVersionInput
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			item, err := deps.MemberAssetService.CreateVersion(r.Context(), principal, assetID, key, input)
			if err != nil {
				writeMemberAssetError(w, err, "asset_version_create_failed")
				return
			}
			writeETag(w, item.ETag)
			writeJSON(w, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

func assetVersionResourceFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		versionID := r.PathValue("versionId")
		if r.Method == http.MethodGet {
			item, err := deps.MemberAssetService.GetVersion(r.Context(), principal, versionID)
			if err != nil {
				writeMemberAssetError(w, err, "asset_version_load_failed")
				return
			}
			writeETag(w, item.ETag)
			writeJSON(w, http.StatusOK, item)
			return
		}
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		if strings.TrimSpace(r.Header.Get("If-Match")) == "" {
			writeError(w, http.StatusPreconditionRequired, "if_match_required")
			return
		}
		var input assetservice.MemberAssetVersionInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		item, err := deps.MemberAssetService.UpdateVersion(r.Context(), principal, versionID, r.Header.Get("If-Match"), key, input)
		if err != nil {
			writeMemberAssetError(w, err, "asset_version_update_failed")
			return
		}
		writeETag(w, item.ETag)
		writeJSON(w, http.StatusOK, item)
	}
}

func restoreAssetFinal(deps Dependencies) http.HandlerFunc {
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
		item, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
		if err != nil {
			writeMemberAssetError(w, err, "asset_load_failed")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, item.WorkspaceID, item.ResourceModelID, "asset.write"); err != nil {
			writeError(w, http.StatusForbidden, "permission_denied")
			return
		}
		if _, err := deps.Store.Pool.Exec(r.Context(), `UPDATE asset.assets SET deleted_at = NULL, publication_status = 'draft', updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, item.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "asset_restore_failed")
			return
		}
		result, err := deps.MemberAssetService.Get(r.Context(), principal, item.ID)
		if err != nil {
			writeMemberAssetError(w, err, "asset_load_failed")
			return
		}
		writeETag(w, result.ETag)
		writeJSON(w, http.StatusOK, result)
	}
}

func duplicateAssetFinal(deps Dependencies) http.HandlerFunc {
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
		original, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
		if err != nil {
			writeMemberAssetError(w, err, "asset_load_failed")
			return
		}
		input := assetservice.MemberAssetInput{ResourceModelID: original.ResourceModelID, Title: original.Title, Markdown: original.Markdown, Fields: original.Fields, Tags: original.Tags, Source: original.Source, Visibility: original.Visibility}
		result, err := deps.MemberAssetService.Create(r.Context(), principal, original.WorkspaceID, key, input)
		if err != nil {
			writeMemberAssetError(w, err, "asset_duplicate_failed")
			return
		}
		writeETag(w, result.ETag)
		writeJSON(w, http.StatusCreated, result)
	}
}
