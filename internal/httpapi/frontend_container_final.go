package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agentchunzhi/internal/container"
)

type finalContainerMoveInput struct {
	ParentID *string `json:"parent_id"`
	SortKey  *string `json:"sort_key"`
}

func moveContainerFinal(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, r.PathValue("containerId")) {
			return
		}
		item, err := deps.ContainerService.Get(r.Context(), principal, r.PathValue("containerId"))
		if err != nil {
			writeContainerError(w, err, "container_load_failed")
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, item.WorkspaceID, "", "container.manage"); err != nil {
			writeError(w, http.StatusForbidden, "permission_denied")
			return
		}
		var input finalContainerMoveInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if input.ParentID != nil && strings.TrimSpace(*input.ParentID) == item.ID {
			writeError(w, http.StatusConflict, "container_cycle")
			return
		}
		if input.ParentID != nil {
			var parentWorkspace, parentStatus string
			if err := deps.Store.Pool.QueryRow(r.Context(), `SELECT workspace_id::text, status FROM content.containers WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, *input.ParentID).Scan(&parentWorkspace, &parentStatus); err != nil {
				if errors.Is(err, container.ErrNotFound) {
					writeError(w, http.StatusNotFound, "container_not_found")
				} else {
					writeError(w, http.StatusNotFound, "container_not_found")
				}
				return
			}
			if parentWorkspace != item.WorkspaceID || parentStatus != "active" {
				writeError(w, http.StatusConflict, "container_cycle")
				return
			}
			var cycle bool
			if err := deps.Store.Pool.QueryRow(r.Context(), `WITH RECURSIVE chain AS (SELECT parent_id FROM content.containers WHERE organization_id = $1::uuid AND id = $2::uuid UNION ALL SELECT c.parent_id FROM content.containers c JOIN chain x ON c.id = x.parent_id WHERE c.organization_id = $1::uuid) SELECT EXISTS (SELECT 1 FROM chain WHERE parent_id = $3::uuid)`, principal.OrganizationID, *input.ParentID, item.ID).Scan(&cycle); err != nil {
				writeError(w, http.StatusInternalServerError, "container_cycle_check_failed")
				return
			}
			if cycle {
				writeError(w, http.StatusConflict, "container_cycle")
				return
			}
		}
		parent := ""
		if input.ParentID != nil {
			parent = strings.TrimSpace(*input.ParentID)
		}
		sortKey := item.SortKey
		if input.SortKey != nil {
			sortKey = strings.TrimSpace(*input.SortKey)
		}
		if _, err := deps.Store.Pool.Exec(r.Context(), `UPDATE content.containers SET parent_id = NULLIF($3, '')::uuid, sort_key = $4, updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, item.ID, parent, sortKey); err != nil {
			writeError(w, http.StatusInternalServerError, "container_move_failed")
			return
		}
		result, err := deps.ContainerService.Get(r.Context(), principal, item.ID)
		if err != nil {
			writeContainerError(w, err, "container_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func containerChildrenFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("containerId")) {
			return
		}
		item, err := deps.ContainerService.Get(r.Context(), principal, r.PathValue("containerId"))
		if err != nil {
			writeContainerError(w, err, "container_load_failed")
			return
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `SELECT id::text, workspace_id::text, parent_id::text, title, sort_key, kind, status, visibility, created_by::text, created_at, updated_at FROM content.containers WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND parent_id = $3::uuid ORDER BY sort_key, title, id`, principal.OrganizationID, item.WorkspaceID, item.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "container_children_failed")
			return
		}
		defer rows.Close()
		items := make([]container.Item, 0)
		for rows.Next() {
			var child container.Item
			if err := rows.Scan(&child.ID, &child.WorkspaceID, &child.ParentID, &child.Name, &child.SortKey, &child.Kind, &child.Status, &child.Visibility, &child.CreatedBy, &child.CreatedAt, &child.UpdatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "container_children_failed")
				return
			}
			items = append(items, child)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "container_children_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func assetContainersFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		assetItem, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
		if err != nil {
			writeMemberAssetError(w, err, "asset_load_failed")
			return
		}
		if r.Method == http.MethodPost {
			key, ok := requestIdempotencyKey(w, r)
			if !ok {
				writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
				return
			}
			var input moveAssetRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			if err := deps.ContainerService.MoveAsset(r.Context(), principal, assetItem.WorkspaceID, assetItem.ID, input.ContainerID, input.Operation, key); err != nil {
				writeContainerError(w, err, "asset_container_update_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `SELECT c.id::text, c.workspace_id::text, c.parent_id::text, c.title, c.sort_key, c.kind, c.status, c.visibility, c.created_by::text, c.created_at, c.updated_at FROM content.container_assets ca JOIN content.containers c ON c.organization_id = ca.organization_id AND c.id = ca.container_id WHERE ca.organization_id = $1::uuid AND ca.workspace_id = $2::uuid AND ca.asset_id = $3::uuid ORDER BY c.sort_key, c.title, c.id`, principal.OrganizationID, assetItem.WorkspaceID, assetItem.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "asset_containers_failed")
			return
		}
		defer rows.Close()
		items := make([]container.Item, 0)
		for rows.Next() {
			var item container.Item
			if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.ParentID, &item.Name, &item.SortKey, &item.Kind, &item.Status, &item.Visibility, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "asset_containers_failed")
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "asset_containers_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func documentChildrenFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		parent, err := deps.MemberAssetService.Get(r.Context(), principal, r.PathValue("assetId"))
		if err != nil {
			writeMemberAssetError(w, err, "asset_load_failed")
			return
		}
		rows, err := deps.Store.Pool.Query(r.Context(), `SELECT child_asset_id::text FROM content.document_parents WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND parent_asset_id = $3::uuid ORDER BY created_at, child_asset_id`, principal.OrganizationID, parent.WorkspaceID, parent.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "document_children_failed")
			return
		}
		defer rows.Close()
		items := make([]any, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				writeError(w, http.StatusInternalServerError, "document_children_failed")
				return
			}
			child, err := deps.MemberAssetService.Get(r.Context(), principal, id)
			if err == nil {
				items = append(items, child)
			}
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "document_children_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}
