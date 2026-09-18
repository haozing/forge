package site

// theme_service.go — 主题修订版服务（站点主题化与 AI 设计重构）。
// 状态机：draft --apply--> draft（站点至多一个）--publish--> published（至多
// 一个，旧的转 archived）。发布 = 原子事务：状态翻转 + 站点指针切换 +
// release 快照 + site.site_changed 事件（缓存失效）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"

	"github.com/jackc/pgx/v5"
)

// ThemeRevision 是主题文件集的一个版本。
type ThemeRevision struct {
	ID          string          `json:"id"`
	SiteID      string          `json:"site_id"`
	RevisionNo  int             `json:"revision_no"`
	Status      string          `json:"status"` // draft | published | archived
	Files       json.RawMessage `json:"files"`
	BaseRevID   string          `json:"base_revision_id,omitempty"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	PublishedAt *time.Time      `json:"published_at,omitempty"`
}

// ErrThemeRevisionNotFound / ErrThemeConflict 是主题域的公共错误。
var (
	ErrThemeRevisionNotFound = ErrSiteNotFound
	ErrThemeConflict         = ErrInvalidInput
)

const themeSelectColumns = `id::text, site_id::text, revision_no, status, files,
	base_revision_id::text, created_by::text, created_at, published_at`

func scanThemeRevision(row pgx.Row) (ThemeRevision, error) {
	var r ThemeRevision
	var base *string
	var published *time.Time
	err := row.Scan(&r.ID, &r.SiteID, &r.RevisionNo, &r.Status, &r.Files,
		&base, &r.CreatedBy, &r.CreatedAt, &published)
	if err != nil {
		return r, err
	}
	if base != nil {
		r.BaseRevID = *base
	}
	r.PublishedAt = published
	return r, nil
}

// ThemeDraft 返回站点当前草稿文件集；无草稿时回退 published 修订版的内容
// （预览/编辑的起点），并以 published 修订版的元数据标识。
func (s *Service) ThemeDraft(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (ThemeRevision, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return ThemeRevision{}, err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return ThemeRevision{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT `+themeSelectColumns+` FROM site.site_theme_revisions
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND status = 'draft'
	`, principal.OrganizationID, siteID)
	r, err := scanThemeRevision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if site.PublishedThemeRevisionID != "" {
			return s.ThemeRevision(ctx, principal, workspaceID, site.PublishedThemeRevisionID)
		}
		return ThemeRevision{SiteID: siteID, Status: "draft", Files: json.RawMessage("{}")}, nil
	}
	return r, err
}

// ThemeRevision 读取单个修订版（含 files）。
func (s *Service) ThemeRevision(ctx context.Context, principal auth.Principal, workspaceID, revisionID string) (ThemeRevision, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return ThemeRevision{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT `+themeSelectColumns+` FROM site.site_theme_revisions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, revisionID)
	return scanThemeRevision(row)
}

// SaveThemeDraft 把文件集写入站点草稿修订版（无则从 published fork，序号顺延）。
// files 必须为合法 JSON 对象；编译与扫描校验由调用方（httpapi，经主题引擎）完成。
func (s *Service) SaveThemeDraft(ctx context.Context, principal auth.Principal, workspaceID, siteID string, files json.RawMessage) (ThemeRevision, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return ThemeRevision{}, err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return ThemeRevision{}, err
	}
	if !json.Valid(files) {
		return ThemeRevision{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ThemeRevision{}, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		SELECT `+themeSelectColumns+` FROM site.site_theme_revisions
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND status = 'draft'
		FOR UPDATE
	`, principal.OrganizationID, siteID)
	draft, err := scanThemeRevision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		var nextNo int
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(revision_no), 0) + 1 FROM site.site_theme_revisions
			WHERE organization_id = $1::uuid AND site_id = $2::uuid
		`, principal.OrganizationID, siteID).Scan(&nextNo); err != nil {
			return ThemeRevision{}, err
		}
		var base any
		if site.PublishedThemeRevisionID != "" {
			base = site.PublishedThemeRevisionID
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO site.site_theme_revisions
				(organization_id, workspace_id, site_id, revision_no, status, files, base_revision_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'draft', $5::jsonb, $6::uuid, $7::uuid)
			RETURNING `+themeSelectColumns,
			principal.OrganizationID, workspaceID, siteID, nextNo, files, base, principal.UserID,
		).Scan(&draft.ID, &draft.SiteID, &draft.RevisionNo, &draft.Status, &draft.Files,
			&draft.BaseRevID, &draft.CreatedBy, &draft.CreatedAt, &draft.PublishedAt)
		if err != nil {
			return ThemeRevision{}, fmt.Errorf("create theme draft: %w", err)
		}
		// 站点 draft 指针必须同步（PublishThemeRevision 依赖它匹配修订版）。
		if _, err := tx.Exec(ctx, `
			UPDATE site.public_sites SET draft_theme_revision_id = $3::uuid
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, principal.OrganizationID, siteID, draft.ID); err != nil {
			return ThemeRevision{}, err
		}
	} else if err != nil {
		return ThemeRevision{}, err
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE site.site_theme_revisions SET files = $2::jsonb, updated_at = now()
			WHERE organization_id = $1::uuid AND id = $3::uuid
		`, principal.OrganizationID, files, draft.ID); err != nil {
			return ThemeRevision{}, fmt.Errorf("update theme draft: %w", err)
		}
		// UPDATE 分支同样保持站点 draft 指针一致。
		if _, err := tx.Exec(ctx, `
			UPDATE site.public_sites SET draft_theme_revision_id = $3::uuid
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, principal.OrganizationID, siteID, draft.ID); err != nil {
			return ThemeRevision{}, err
		}
	}

	if err := appendSiteEvent(ctx, tx, s.Events, principal, workspaceID, site, "theme_draft_saved"); err != nil {
		return ThemeRevision{}, err
	}
	recordSiteAudit(ctx, tx, principal, workspaceID, "site.theme_saved", siteID, map[string]any{
		"revision_id": draft.ID, "revision_no": draft.RevisionNo,
	})
	if err := tx.Commit(ctx); err != nil {
		return ThemeRevision{}, err
	}
	draft.Files = files
	return draft, nil
}

// ListThemeRevisions 返回修订版历史（published/archived 元数据，不含 files）。
func (s *Service) ListThemeRevisions(ctx context.Context, principal auth.Principal, workspaceID, siteID string) ([]ThemeRevision, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, site_id::text, revision_no, status,
		       '{}'::jsonb, NULL::uuid, created_by::text, created_at, published_at
		FROM site.site_theme_revisions
		WHERE organization_id = $1::uuid AND site_id = $2::uuid
		ORDER BY revision_no DESC
	`, principal.OrganizationID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ThemeRevision{}
	for rows.Next() {
		var r ThemeRevision
		var base *string
		var published *time.Time
		if err := rows.Scan(&r.ID, &r.SiteID, &r.RevisionNo, &r.Status,
			&r.Files, &base, &r.CreatedBy, &r.CreatedAt, &published); err != nil {
			return nil, err
		}
		r.PublishedAt = published
		out = append(out, r)
	}
	return out, rows.Err()
}

// PublishThemeRevision 把一个修订版发布为站点当前主题（回滚 = 直接发布历史
// 修订版，序号顺延产生新 published 记录）。发布创建 release 快照并发缓存失效。
func (s *Service) PublishThemeRevision(ctx context.Context, principal auth.Principal, workspaceID, siteID, revisionID string) (ThemeRevision, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteLifecycle); err != nil {
		return ThemeRevision{}, err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return ThemeRevision{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ThemeRevision{}, err
	}
	defer tx.Rollback(ctx)

	// 目标修订版必须是本站点可发布的（draft 或历史 published/archived）。
	var files json.RawMessage
	var status string
	var newRevID string
	err = tx.QueryRow(ctx, `
		SELECT status, files FROM site.site_theme_revisions
		WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, principal.OrganizationID, revisionID).Scan(&status, &files)
	if errors.Is(err, pgx.ErrNoRows) {
		return ThemeRevision{}, ErrSiteNotFound
	}
	if err != nil {
		return ThemeRevision{}, err
	}
	if status == "draft" {
		if site.DraftThemeRevisionID == "" || site.DraftThemeRevisionID != revisionID {
			return ThemeRevision{}, ErrSiteNotFound
		}
	}

	// 旧 published → archived；目标转 published（draft 消耗掉）。
	if _, err := tx.Exec(ctx, `
		UPDATE site.site_theme_revisions SET status = 'archived'
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND status = 'published'
	`, principal.OrganizationID, siteID); err != nil {
		return ThemeRevision{}, err
	}
	if status == "draft" {
		if _, err := tx.Exec(ctx, `
			UPDATE site.site_theme_revisions SET status = 'published', published_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, principal.OrganizationID, revisionID); err != nil {
			return ThemeRevision{}, err
		}
		newRevID = revisionID
	} else {
		// 历史修订版重发布：新序号副本（原修订版保持 archived，审计可溯）。
		var nextNo int
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(revision_no), 0) + 1 FROM site.site_theme_revisions
			WHERE organization_id = $1::uuid AND site_id = $2::uuid
		`, principal.OrganizationID, siteID).Scan(&nextNo); err != nil {
			return ThemeRevision{}, err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO site.site_theme_revisions
				(organization_id, workspace_id, site_id, revision_no, status, files, base_revision_id, created_by, published_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'published', $5::jsonb, $6::uuid, $7::uuid, now())
			RETURNING id::text
		`, principal.OrganizationID, workspaceID, siteID, nextNo, files, revisionID, principal.UserID,
		).Scan(&newRevID); err != nil {
			return ThemeRevision{}, fmt.Errorf("republish theme revision: %w", err)
		}
	}

	// 站点指针 + revision bump。
	if _, err := tx.Exec(ctx, `
		UPDATE site.public_sites
		SET published_theme_revision_id = $3::uuid,
		    draft_theme_revision_id = $3::uuid,
		    revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, siteID, newRevID); err != nil {
		return ThemeRevision{}, err
	}

	// release 快照钉住主题修订版，并成为站点的当前发布指针。
	if _, err := tx.Exec(ctx, `
		INSERT INTO site.site_releases (organization_id, workspace_id, site_id, revision, config, published_by)
		SELECT organization_id, workspace_id, id,
		       (SELECT COALESCE(MAX(revision), 0) + 1 FROM site.site_releases r
		         WHERE r.organization_id = site.public_sites.organization_id AND r.site_id = site.public_sites.id),
		       jsonb_build_object('theme_revision_id', $3::uuid), $4::uuid
		FROM site.public_sites
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, siteID, newRevID, principal.UserID); err != nil {
		return ThemeRevision{}, fmt.Errorf("create theme release: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE site.public_sites s
		SET published_release_id = (
			SELECT r.id FROM site.site_releases r
			WHERE r.organization_id = s.organization_id AND r.site_id = s.id
			  AND r.revision = (SELECT COALESCE(MAX(revision), 0) FROM site.site_releases r2
			                    WHERE r2.organization_id = s.organization_id AND r2.site_id = s.id)
		)
		WHERE s.organization_id = $1::uuid AND s.id = $2::uuid
	`, principal.OrganizationID, siteID); err != nil {
		return ThemeRevision{}, fmt.Errorf("point published release: %w", err)
	}

	updated, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return ThemeRevision{}, err
	}
	if err := appendSiteEvent(ctx, tx, s.Events, principal, workspaceID, updated, "theme_published"); err != nil {
		return ThemeRevision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThemeRevision{}, err
	}
	return s.ThemeRevision(ctx, principal, workspaceID, newRevID)
}

// TrimThemeSlot 供 httpapi 校验槽位名。
func TrimThemeSlot(slot string) string {
	return strings.TrimSpace(slot)
}
