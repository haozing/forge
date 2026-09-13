package site

// design_session.go v2 — AI 设计会话沙盒（主题化重构）。
// 会话持有主题文件集快照（fork 自站点 draft/published 修订版）；
// write_theme 落在会话 files；apply 把通过编译+扫描的文件集写入
// 站点 draft 修订版；discard 零残留。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/theme"

	"github.com/jackc/pgx/v5"
)

// DesignSession 是一次设计会话的沙盒。
type DesignSession struct {
	ID        string          `json:"id"`
	SiteID    string          `json:"site_id"`
	Status    string          `json:"status"`
	Files     json.RawMessage `json:"files"`
	BaseRevID string          `json:"base_theme_revision_id,omitempty"`
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Issues    []string        `json:"issues,omitempty"`
}

const sessionColumns = `id::text, site_id::text, status, files,
	COALESCE(base_theme_revision_id::text, ''), created_by::text, created_at, updated_at`

func scanSession(row pgx.Row) (DesignSession, error) {
	var s DesignSession
	err := row.Scan(&s.ID, &s.SiteID, &s.Status, &s.Files,
		&s.BaseRevID, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// StartDesignSession fork 站点 draft（否则 published）文件集开一个新会话。
func (s Service) StartDesignSession(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (DesignSession, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return DesignSession{}, err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return DesignSession{}, err
	}
	files := json.RawMessage("{}")
	var base any
	if site.DraftThemeRevisionID != "" {
		err = s.Store.Pool.QueryRow(ctx, `SELECT files FROM site.site_theme_revisions WHERE id = $1::uuid`, site.DraftThemeRevisionID).Scan(&files)
		if err == nil {
			base = site.DraftThemeRevisionID
		}
	} else if site.PublishedThemeRevisionID != "" {
		err = s.Store.Pool.QueryRow(ctx, `SELECT files FROM site.site_theme_revisions WHERE id = $1::uuid`, site.PublishedThemeRevisionID).Scan(&files)
		if err == nil {
			base = site.PublishedThemeRevisionID
		}
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		INSERT INTO site.design_sessions (organization_id, workspace_id, site_id, files, base_theme_revision_id, created_by)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::jsonb, $5::uuid, $6::uuid
		WHERE NOT EXISTS (
			SELECT 1 FROM site.design_sessions
			WHERE organization_id = $1::uuid AND site_id = $3::uuid AND status = 'open'
		)
	`, principal.OrganizationID, workspaceID, siteID, files, base, principal.UserID); err != nil {
		return DesignSession{}, fmt.Errorf("start design session: %w", err)
	}
	var session DesignSession
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT `+sessionColumns+` FROM site.design_sessions
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND status = 'open'
	`, principal.OrganizationID, siteID).Scan(&session.ID, &session.SiteID, &session.Status,
		&session.Files, &session.BaseRevID, &session.CreatedBy, &session.CreatedAt, &session.UpdatedAt)
	return session, err
}

// GetDesignSession 读会话（含 files 与主题校验 issues）。
func (s Service) GetDesignSession(ctx context.Context, principal auth.Principal, workspaceID, sessionID string) (DesignSession, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return DesignSession{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT `+sessionColumns+` FROM site.design_sessions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, sessionID)
	session, err := scanSession(row)
	if err != nil {
		return DesignSession{}, err
	}
	session.Issues = s.themeIssues(session.Files)
	return session, nil
}

// SaveDesignSessionFiles 整体替换会话文件集（write_theme 的落点）。
func (s Service) SaveDesignSessionFiles(ctx context.Context, principal auth.Principal, workspaceID, sessionID string, files json.RawMessage) (DesignSession, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return DesignSession{}, err
	}
	if !json.Valid(files) {
		return DesignSession{}, ErrInvalidInput
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE site.design_sessions SET files = $3::jsonb, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'open'
	`, principal.OrganizationID, sessionID, files)
	if err != nil {
		return DesignSession{}, err
	}
	if tag.RowsAffected() == 0 {
		return DesignSession{}, ErrSiteNotFound
	}
	return s.GetDesignSession(ctx, principal, workspaceID, sessionID)
}

// ApplyDesignSession 把会话文件集经编译+扫描校验后写入站点 draft 修订版。
func (s Service) ApplyDesignSession(ctx context.Context, principal auth.Principal, workspaceID, siteID, sessionID string) (Site, error) {
	session, err := s.GetDesignSession(ctx, principal, workspaceID, sessionID)
	if err != nil {
		return Site{}, err
	}
	if session.Status != "open" {
		return Site{}, ErrInvalidInput
	}
	var files map[string]string
	if err := json.Unmarshal(session.Files, &files); err != nil {
		return Site{}, ErrInvalidInput
	}
	// 编译 + 扫描门禁（fail-closed；问题清单随 GetDesignSession.issues 暴露）。
	if _, cerr := theme.Compile(files, theme.Options{}); cerr != nil {
		return Site{}, ErrInvalidInput
	}
	if _, err := s.SaveThemeDraft(ctx, principal, workspaceID, siteID, session.Files); err != nil {
		return Site{}, err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE site.design_sessions SET status = 'applied', updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, sessionID)
	if err != nil {
		return Site{}, err
	}
	if tag.RowsAffected() == 0 {
		return Site{}, ErrSiteNotFound
	}
	return s.GetSite(ctx, principal, workspaceID, siteID)
}

// DiscardDesignSession 放弃会话（status=discarded，零残留）。
func (s Service) DiscardDesignSession(ctx context.Context, principal auth.Principal, workspaceID, sessionID string) error {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE site.design_sessions SET status = 'discarded', updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status <> 'discarded'
	`, principal.OrganizationID, sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSiteNotFound
	}
	return nil
}

// themeIssues 对文件集跑编译+扫描，返回可读问题清单（空 = 通过）。
func (s Service) themeIssues(files json.RawMessage) []string {
	var m map[string]string
	if err := json.Unmarshal(files, &m); err != nil {
		return []string{"files 不是合法的 JSON 对象"}
	}
	_, cerr := theme.Compile(m, theme.Options{})
	if cerr == nil {
		return nil
	}
	var ce *theme.CompileError
	if errors.As(cerr, &ce) {
		out := make([]string, 0, len(ce.Problems))
		for _, p := range ce.Problems {
			out = append(out, fmt.Sprintf("%s:%d [%s] %s", p.File, p.Line, p.Rule, p.Detail))
		}
		return out
	}
	return []string{cerr.Error()}
}
