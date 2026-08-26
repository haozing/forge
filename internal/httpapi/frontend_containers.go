package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"agentchunzhi/internal/container"
)

func writeContainerError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, container.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, container.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, container.ErrNotFound):
		writeError(w, http.StatusNotFound, "container_not_found")
	case errors.Is(err, container.ErrConflict):
		writeError(w, http.StatusConflict, "container_not_empty_or_cycle")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func containerTree(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.ContainerService.Tree(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeContainerError(w, err, "container_tree_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func createContainer(deps Dependencies) http.HandlerFunc {
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
		var input container.CreateInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		item, err := deps.ContainerService.Create(r.Context(), principal, r.PathValue("workspaceId"), input)
		if err != nil {
			writeContainerError(w, err, "container_create_failed")
			return
		}
		_ = key
		writeJSON(w, http.StatusCreated, item)
	}
}

func containersCollection(deps Dependencies) http.HandlerFunc {
	tree := containerTree(deps)
	create := createContainer(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			tree(w, r)
			return
		}
		create(w, r)
	}
}

func getContainer(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.ContainerService.Get(r.Context(), principal, r.PathValue("containerId"))
		if err != nil {
			writeContainerError(w, err, "container_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func patchContainer(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		var input container.PatchInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		item, err := deps.ContainerService.Patch(r.Context(), principal, r.PathValue("containerId"), input)
		if err != nil {
			writeContainerError(w, err, "container_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func containerResource(deps Dependencies) http.HandlerFunc {
	get := getContainer(deps)
	patch := patchContainer(deps)
	remove := deleteContainer(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			remove(w, r)
			return
		}
		patch(w, r)
	}
}

func deleteContainer(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requiredIdempotencyKey(r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		if err := deps.ContainerService.Delete(r.Context(), principal, r.PathValue("containerId")); err != nil {
			writeContainerError(w, err, "container_delete_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func listContainerAssets(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.ContainerService.Assets(r.Context(), principal, r.PathValue("containerId"))
		if err != nil {
			writeContainerError(w, err, "container_assets_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

type moveAssetRequest struct {
	ContainerID string `json:"container_id"`
	Operation   string `json:"operation"`
}

func moveAssetToContainer(deps Dependencies) http.HandlerFunc {
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
		var input moveAssetRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if workspaceID == "" {
			asset, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
			if err != nil {
				writeMemberAssetError(w, err, "asset_load_failed")
				return
			}
			workspaceID = asset.WorkspaceID
		}
		if err := deps.ContainerService.MoveAsset(r.Context(), principal, workspaceID, r.PathValue("assetId"), input.ContainerID, input.Operation, key); err != nil {
			writeContainerError(w, err, "asset_move_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

type documentParentRequest struct {
	ParentAssetID *string `json:"parent_asset_id"`
}

func setDocumentParent(deps Dependencies) http.HandlerFunc {
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
		var input documentParentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if workspaceID == "" {
			asset, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
			if err != nil {
				writeMemberAssetError(w, err, "asset_load_failed")
				return
			}
			workspaceID = asset.WorkspaceID
		}
		if err := deps.ContainerService.SetDocumentParent(r.Context(), principal, workspaceID, r.PathValue("assetId"), input.ParentAssetID, key); err != nil {
			writeContainerError(w, err, "document_parent_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
func deleteDocumentParent(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
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
		workspaceID := r.PathValue("workspaceId")
		if workspaceID == "" {
			asset, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
			if err != nil {
				writeMemberAssetError(w, err, "asset_load_failed")
				return
			}
			workspaceID = asset.WorkspaceID
		}
		if err := deps.ContainerService.SetDocumentParent(r.Context(), principal, workspaceID, r.PathValue("assetId"), nil, key); err != nil {
			writeContainerError(w, err, "document_parent_delete_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func documentParentResource(deps Dependencies) http.HandlerFunc {
	set := setDocumentParent(deps)
	remove := deleteDocumentParent(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			set(w, r)
			return
		}
		remove(w, r)
	}
}
