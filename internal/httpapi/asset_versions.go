package httpapi

import (
	"net/http"

	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/deletion"
)

func assetResource(deps Dependencies) http.HandlerFunc {
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

func assetVersionCollection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		assetID := r.PathValue("assetId")
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		items, err := deps.MemberAssetService.ListVersions(r.Context(), principal, assetID)
		if err != nil {
			writeMemberAssetError(w, err, "asset_versions_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func assetVersionResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		versionID := r.PathValue("versionId")
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		item, err := deps.MemberAssetService.GetVersion(r.Context(), principal, versionID)
		if err != nil {
			writeMemberAssetError(w, err, "asset_version_load_failed")
			return
		}
		writeETag(w, item.ETag)
		writeJSON(w, http.StatusOK, item)
	}
}

func duplicateAsset(deps Dependencies) http.HandlerFunc {
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
		input := assetservice.MemberAssetInput{ResourceModelID: original.ResourceModelID, Title: original.Title, Markdown: original.Markdown, Fields: original.Fields, Visibility: original.Visibility}
		result, err := deps.MemberAssetService.Create(r.Context(), principal, original.WorkspaceID, key, input)
		if err != nil {
			writeMemberAssetError(w, err, "asset_duplicate_failed")
			return
		}
		writeETag(w, result.ETag)
		writeJSON(w, http.StatusCreated, result)
	}
}
