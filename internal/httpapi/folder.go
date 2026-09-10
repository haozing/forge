package httpapi

// folder.go — 目录树 HTTP 面：note-containers（"我的笔记"目录）与
// doc-containers（知识库分类）。两个资源族共用同一套 handler，仅 kind
// 不同。读走 asset.read（全角色可见组织结构）；增删改走 container.manage
// （admin/editor，角色矩阵既有动作）。权限判定全部经 requireWorkspaceAction
// 的工作区策略，服务层不做角色判断。

import (
	"errors"
	"net/http"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/folder"
)

type CreateFolderRequest struct {
	Title    string `json:"title"`
	ParentID string `json:"parent_id"`
}

type PatchFolderRequest struct {
	Title       *string `json:"title,omitempty"`
	ParentID    *string `json:"parent_id,omitempty"`
	SetParentID bool    `json:"set_parent_id,omitempty"`
	SortKey     *string `json:"sort_key,omitempty"`
}

type AssignNoteContainerRequest struct {
	// ContainerID 为空串表示从所有目录摘下（回到未分组）。
	ContainerID string `json:"container_id"`
}

// FolderError maps folder domain errors onto the HTTP status/code contract.
func FolderError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, folder.ErrNotFound):
		writeError(w, http.StatusNotFound, "folder_not_found")
	case errors.Is(err, folder.ErrNotEmpty):
		writeError(w, http.StatusConflict, "folder_not_empty")
	case errors.Is(err, folder.ErrInvalidParent):
		writeError(w, http.StatusUnprocessableEntity, "folder_invalid_parent")
	case errors.Is(err, folder.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, folder.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, authz.ErrWorkspaceNotFound):
		writeError(w, http.StatusNotFound, "resource_not_found")
	case errors.Is(err, authz.ErrWorkspaceForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

// folderCollection serves GET/POST for one folder tree family.
func folderCollection(deps Dependencies, kind folder.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionAssetRead) {
				return
			}
			items, err := deps.FolderService.List(r.Context(), principal, workspaceID, kind)
			if err != nil {
				FolderError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionContainerManage) {
				return
			}
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input CreateFolderRequest
			if !decodeBody(w, r, &input, 8*1024) {
				return
			}
			item, err := deps.FolderService.Create(r.Context(), principal, workspaceID, kind, input.Title, input.ParentID)
			if err != nil {
				FolderError(w, err)
				return
			}
			writeData(w, r, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// folderResource serves PATCH/DELETE for one folder.
func folderResource(deps Dependencies, kind folder.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		containerID := r.PathValue("containerId")
		if !requirePathUUID(w, workspaceID, containerID) {
			return
		}
		switch r.Method {
		case http.MethodPatch:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionContainerManage) {
				return
			}
			// Decode into the tagged request struct: folder.PatchInput has no
			// json tags, so snake_case keys would silently never bind.
			var body PatchFolderRequest
			if !decodeBody(w, r, &body, 8*1024) {
				return
			}
			input := folder.PatchInput{
				Title:       body.Title,
				ParentID:    body.ParentID,
				ParentIDSet: body.SetParentID || body.ParentID != nil,
				SortKey:     body.SortKey,
			}
			item, err := deps.FolderService.Patch(r.Context(), principal, workspaceID, kind, containerID, input)
			if err != nil {
				FolderError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, item)
		case http.MethodDelete:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionContainerManage) {
				return
			}
			if err := deps.FolderService.Delete(r.Context(), principal, workspaceID, kind, containerID); err != nil {
				FolderError(w, err)
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{"deleted": true})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// NoteFolderCollection / NoteFolderResource — "我的笔记"目录树。
func NoteFolderCollection(deps Dependencies) http.HandlerFunc {
	return folderCollection(deps, folder.KindNote)
}

func NoteFolderResource(deps Dependencies) http.HandlerFunc {
	return folderResource(deps, folder.KindNote)
}

// DocFolderCollection / DocFolderResource — 知识库分类树。
func DocFolderCollection(deps Dependencies) http.HandlerFunc {
	return folderCollection(deps, folder.KindDoc)
}

func DocFolderResource(deps Dependencies) http.HandlerFunc {
	return folderResource(deps, folder.KindDoc)
}

// noteContainerResource serves PUT /api/conversations/{conversationId}/
// note-container — 把该会话绑定的灵感笔记挂进/摘出目录。仅笔记所属者可操作
// （服务层校验 initiator），目录必须存在且为 note_folder。
func noteContainerResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		conversationID := r.PathValue("conversationId")
		if !requirePathUUID(w, conversationID) {
			return
		}
		var input AssignNoteContainerRequest
		if !decodeBody(w, r, &input, 4*1024) {
			return
		}
		if _, err := deps.FolderService.AssignNote(r.Context(), principal, conversationID, input.ContainerID); err != nil {
			FolderError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"conversation_id":   conversationID,
			"note_container_id": input.ContainerID,
		})
	}
}
