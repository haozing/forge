package folder

// folder.go — "我的笔记"与"知识库"的多级目录树。目录是 content.containers
// 的组织性子域：note_folder 承载会话绑定的灵感笔记，doc_folder 承载知识库
// 分类。目录本身没有 asset、没有版本——父子关系走 parent_id，同层排序走
// sort_key，笔记/文档经 content.container_assets 挂到目录上。
//
// 权限判定在 handler（读 asset.read、管理 container.manage，均为角色矩阵
// 既有动作）；本层只做组织域的 SQL 约束：同工作区、同 kind、active 状态、
// 深度护栏与移动时的环检测。

import (
	"context"
	"errors"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// 树深护栏：16 层足够表达任何现实分类，同时让祖先链遍历成本有界。
const maxDepth = 16

// 目录标题与排序键的长度上限（rune 计）。
const (
	maxTitleRunes   = 200
	maxSortKeyRunes = 128
)

var (
	ErrNotFound      = errors.New("folder not found")
	ErrInvalidInput  = errors.New("invalid folder input")
	ErrInvalidParent = errors.New("invalid folder parent")
	ErrForbidden     = errors.New("folder access denied")
	ErrNotEmpty      = errors.New("folder not empty")
)

type Kind string

const (
	KindNote Kind = "note_folder"
	KindDoc  Kind = "doc_folder"
)

func (k Kind) valid() bool { return k == KindNote || k == KindDoc }

type Folder struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Title       string    `json:"title"`
	ParentID    string    `json:"parent_id"`
	SortKey     string    `json:"sort_key"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PatchInput 遵循"指针 = 显式设置"约定：ParentIDSet 区分"移动到根"
// （parent_id=""）与"不移动"（字段缺省）。
type PatchInput struct {
	Title       *string
	ParentID    *string
	ParentIDSet bool
	SortKey     *string
}

type Service struct {
	Store *store.Store
}

func (s Service) validID(value string) bool {
	return len(value) == 36 && strings.Count(value, "-") == 4
}

func validTitle(title string) bool {
	runes := len([]rune(title))
	return runes > 0 && runes <= maxTitleRunes
}

func validSortKey(key string) bool {
	return len([]rune(key)) <= maxSortKeyRunes
}

const folderColumns = `id::text, workspace_id::text, title, COALESCE(parent_id::text, ''),
	sort_key, created_at, updated_at`

func scanFolder(row pgx.Row) (Folder, error) {
	var f Folder
	if err := row.Scan(&f.ID, &f.WorkspaceID, &f.Title, &f.ParentID, &f.SortKey, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return Folder{}, err
	}
	return f, nil
}

// List 返回平铺的目录清单（前端组树）。深度/层级约束在写路径保证，读路径
// 不做递归展开——清单规模远小于一次递归查询的成本。
func (s Service) List(ctx context.Context, principal auth.Principal, workspaceID string, kind Kind) ([]Folder, error) {
	if !kind.valid() || !s.validID(workspaceID) {
		return nil, ErrInvalidInput
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT `+folderColumns+`
		FROM content.containers
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		  AND kind = $3 AND status = 'active'
		ORDER BY sort_key, created_at, id
	`, principal.OrganizationID, workspaceID, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Folder, 0)
	for rows.Next() {
		f, scanErr := scanFolder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

func (s Service) Get(ctx context.Context, principal auth.Principal, workspaceID string, kind Kind, containerID string) (Folder, error) {
	if !kind.valid() || !s.validID(workspaceID) || !s.validID(containerID) {
		return Folder{}, ErrInvalidInput
	}
	f, err := scanFolder(s.Store.Pool.QueryRow(ctx, `
		SELECT `+folderColumns+`
		FROM content.containers
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		  AND kind = $3 AND id = $4::uuid AND status = 'active'
	`, principal.OrganizationID, workspaceID, string(kind), containerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	return f, err
}

// Create 新建目录。parentID 为空表示建在根层。
func (s Service) Create(ctx context.Context, principal auth.Principal, workspaceID string, kind Kind, title, parentID string) (Folder, error) {
	if !kind.valid() || !s.validID(workspaceID) {
		return Folder{}, ErrInvalidInput
	}
	title = strings.TrimSpace(title)
	if !validTitle(title) {
		return Folder{}, ErrInvalidInput
	}
	if parentID != "" && !s.validID(parentID) {
		return Folder{}, ErrInvalidInput
	}
	if parentID != "" {
		if _, err := s.ancestorCheck(ctx, principal, workspaceID, kind, parentID, ""); err != nil {
			return Folder{}, err
		}
	}
	f, err := scanFolder(s.Store.Pool.QueryRow(ctx, `
		INSERT INTO content.containers
			(organization_id, workspace_id, kind, title, parent_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5::text, '')::uuid, $6::uuid)
		RETURNING `+folderColumns+`
	`, principal.OrganizationID, workspaceID, string(kind), title, parentID, principal.UserID))
	if err != nil {
		return Folder{}, err
	}
	return f, nil
}

// Patch 重命名/移动/改排序。移动做两道校验：新父不能是自己或自己的后代
// （沿祖先链向上走，遇到自己即成环），且落点深度不越界。
func (s Service) Patch(ctx context.Context, principal auth.Principal, workspaceID string, kind Kind, containerID string, input PatchInput) (Folder, error) {
	if !kind.valid() || !s.validID(containerID) {
		return Folder{}, ErrInvalidInput
	}
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if !validTitle(title) {
			return Folder{}, ErrInvalidInput
		}
	}
	if input.SortKey != nil && !validSortKey(*input.SortKey) {
		return Folder{}, ErrInvalidInput
	}
	current, err := s.Get(ctx, principal, workspaceID, kind, containerID)
	if err != nil {
		return Folder{}, err
	}
	newParent := current.ParentID
	if input.ParentIDSet {
		newParent = strings.TrimSpace(*input.ParentID)
		if newParent != "" && !s.validID(newParent) {
			return Folder{}, ErrInvalidInput
		}
		if newParent == containerID {
			return Folder{}, ErrInvalidParent
		}
		if newParent != "" {
			// 祖先链上遇到自己 = 移进自己的子树，成环。
			if _, err := s.ancestorCheck(ctx, principal, workspaceID, kind, newParent, containerID); err != nil {
				return Folder{}, err
			}
		}
	}
	if input.Title == nil && !input.ParentIDSet && input.SortKey == nil {
		return current, nil
	}
	f, err := scanFolder(s.Store.Pool.QueryRow(ctx, `
		UPDATE content.containers SET
			title = COALESCE($3, title),
			parent_id = CASE WHEN $4::bool THEN NULLIF($5::text, '')::uuid ELSE parent_id END,
			sort_key = COALESCE($6, sort_key),
			updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
		RETURNING `+folderColumns+`
	`, principal.OrganizationID, containerID, input.Title, input.ParentIDSet, newParent, input.SortKey))
	if err != nil {
		return Folder{}, err
	}
	return f, nil
}

// Delete 移除目录。仅允许删空目录：有子目录或仍挂着资产时返回 ErrNotEmpty，
// 让前端引导用户先清空——递归删除会造成无法恢复的内容丢失。
func (s Service) Delete(ctx context.Context, principal auth.Principal, workspaceID string, kind Kind, containerID string) error {
	if !kind.valid() || !s.validID(workspaceID) || !s.validID(containerID) {
		return ErrInvalidInput
	}
	if _, err := s.Get(ctx, principal, workspaceID, kind, containerID); err != nil {
		return err
	}
	var childFolders bool
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM content.containers
			WHERE organization_id = $1::uuid AND parent_id = $2::uuid AND status = 'active'
		)
	`, principal.OrganizationID, containerID).Scan(&childFolders); err != nil {
		return err
	}
	if childFolders {
		return ErrNotEmpty
	}
	var attached bool
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM content.container_assets
			WHERE organization_id = $1::uuid AND container_id = $2::uuid
		)
	`, principal.OrganizationID, containerID).Scan(&attached); err != nil {
		return err
	}
	if attached {
		return ErrNotEmpty
	}
	cmd, err := s.Store.Pool.Exec(ctx, `
		DELETE FROM content.containers
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
	`, principal.OrganizationID, containerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AssignNote 把会话绑定的灵感笔记挂到 note_folder 目录（或从目录摘下）。
// 仅笔记所属者可操作：产品约定灵感卡仅自己可见，目录归属随笔记所有者走。
// 笔记同一时刻只属于一个目录（container_assets 的单成员语义）。
func (s Service) AssignNote(ctx context.Context, principal auth.Principal, conversationID, containerID string) (string, error) {
	if !s.validID(conversationID) {
		return "", ErrInvalidInput
	}
	if containerID != "" && !s.validID(containerID) {
		return "", ErrInvalidInput
	}
	var organizationID, workspaceID, initiator, noteAssetID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT c.organization_id::text, c.workspace_id::text,
		       c.initiator_user_id::text, COALESCE(nb.note_asset_id::text, '')
		FROM content.conversations c
		JOIN content.note_bindings nb ON nb.conversation_id = c.id
		WHERE c.id = $1::uuid AND c.organization_id = $2::uuid AND c.status <> 'archived'
	`, conversationID, principal.OrganizationID).Scan(&organizationID, &workspaceID, &initiator, &noteAssetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if initiator != principal.UserID || noteAssetID == "" {
		return "", ErrForbidden
	}
	if containerID != "" {
		var ok bool
		if err := s.Store.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM content.containers
				WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
				  AND id = $3::uuid AND kind = $4 AND status = 'active'
			)
		`, organizationID, workspaceID, containerID, string(KindNote)).Scan(&ok); err != nil {
			return "", err
		}
		if !ok {
			return "", ErrInvalidParent
		}
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		DELETE FROM content.container_assets
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		  AND container_id IN (SELECT id FROM content.containers WHERE organization_id = $1::uuid AND kind = $3)
	`, organizationID, noteAssetID, string(KindNote)); err != nil {
		return "", err
	}
	if containerID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO content.container_assets
				(organization_id, workspace_id, container_id, asset_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid)
			ON CONFLICT (container_id, asset_id) DO NOTHING
		`, organizationID, workspaceID, containerID, noteAssetID, principal.UserID); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return containerID, nil
}

// ancestorCheck 沿 parent 链向上走：校验每个祖先存在且为同 kind 的 active
// 目录，累计深度不越界；stopID 非空时（移动场景）链上遇到它即判定成环。
// 返回 parent 所在深度（根层子节点为 0）。
func (s Service) ancestorCheck(ctx context.Context, principal auth.Principal, workspaceID string, kind Kind, parentID, stopID string) (int, error) {
	current := parentID
	steps := 0
	for current != "" {
		if steps >= maxDepth {
			return 0, ErrInvalidParent
		}
		if stopID != "" && current == stopID {
			return 0, ErrInvalidParent
		}
		var next string
		err := s.Store.Pool.QueryRow(ctx, `
			SELECT COALESCE(parent_id::text, '')
			FROM content.containers
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND id = $3::uuid AND kind = $4 AND status = 'active'
		`, principal.OrganizationID, workspaceID, current, string(kind)).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrInvalidParent
		}
		if err != nil {
			return 0, err
		}
		current = next
		steps += 1
	}
	return steps, nil
}
