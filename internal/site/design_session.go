package site

// design_session.go — AI 设计会话沙盒（站点方案 D/§10，2026-09-13）。
// 语义：Start 把站点工作配置 fork 成 session_config；agent 的 apply_patch
// 只改沙盒（过模块目录校验），全程不写站点行；"应用"是人审 diff 后把沙盒
// 一次性写回站点（site.design）——与《成员与 Agent 权限统一方案》J
// （agent 无 site.publish / 不写站点行）一致。观察（observe）在
// RENDERER_ENABLED 未开时降级为结构化观察（token/块统计/校验问题清单）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"

	"github.com/jackc/pgx/v5"
)

// DesignSession is one forked design sandbox.
type DesignSession struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	WorkspaceID    string          `json:"workspace_id"`
	SiteID         string          `json:"site_id"`
	SessionConfig  json.RawMessage `json:"session_config"`
	Status         string          `json:"status"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// StartDesignSession forks the site's working design config into a sandbox
// session. 权限：site.read + 可用 agent 应用（成员本人即满足；agent 身份
// 不直接建会话——会话由成员发起）。
func (s Service) StartDesignSession(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (DesignSession, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return DesignSession{}, err
	}
	current, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return DesignSession{}, err
	}
	var id string
	err = s.Store.Pool.QueryRow(ctx, `
		INSERT INTO site.design_sessions
			(organization_id, workspace_id, site_id, session_config, base_pages_config, base_style_config, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid,
		        CASE WHEN jsonb_typeof($4::jsonb->'home'->'blocks') = 'array' THEN $4::jsonb
		             ELSE jsonb_set($4::jsonb, '{home}', '{"blocks":[]}'::jsonb, true) END,
		        $5::jsonb, $6::jsonb, $7::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, siteID,
		mustMarshalJSON(PagesConfigFork(current.PagesConfig, current.HomepageConfig)),
		current.PagesConfig, current.StyleConfig, principal.UserID).Scan(&id)
	if err != nil {
		return DesignSession{}, fmt.Errorf("start design session: %w", err)
	}
	return s.GetDesignSession(ctx, principal, workspaceID, id)
}

// GetDesignSession reads one active sandbox session.
func (s Service) GetDesignSession(ctx context.Context, principal auth.Principal, workspaceID, sessionID string) (DesignSession, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return DesignSession{}, err
	}
	var item DesignSession
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, workspace_id::text, site_id::text,
		       session_config, status, created_at, updated_at
		FROM site.design_sessions
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
	`, principal.OrganizationID, workspaceID, sessionID).Scan(
		&item.ID, &item.OrganizationID, &item.WorkspaceID, &item.SiteID,
		&item.SessionConfig, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return DesignSession{}, ErrSiteNotFound
		}
		return DesignSession{}, fmt.Errorf("load design session: %w", err)
	}
	return item, nil
}

// ApplyDesignPatch mutates the sandbox config (NOT the site row). The patch
// is a full pages_config document; it passes the same closed-catalog
// validation as the site write path.
func (s Service) ApplyDesignPatch(ctx context.Context, principal auth.Principal, workspaceID, sessionID string, patch json.RawMessage) (DesignSession, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return DesignSession{}, err
	}
	var config PagesConfig
	if err := json.Unmarshal(patch, &config); err != nil {
		return DesignSession{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return DesignSession{}, fmt.Errorf("begin design patch: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := ValidatePagesConfig(ctx, tx, principal.OrganizationID, workspaceID, config); err != nil {
		return DesignSession{}, err
	}
	var status string
	err = tx.QueryRow(ctx, `
		UPDATE site.design_sessions
		SET session_config = $3::jsonb, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
		RETURNING status
	`, principal.OrganizationID, sessionID, string(patch)).Scan(&status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return DesignSession{}, ErrSiteNotFound
		}
		return DesignSession{}, fmt.Errorf("apply design patch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return DesignSession{}, err
	}
	return s.GetDesignSession(ctx, principal, workspaceID, sessionID)
}

// ApplyDesignSession is the HUMAN step: 写回站点工作配置（site.design）。
// agent 永远不调用本方法；发布仍走既有 release 流程。
func (s Service) ApplyDesignSession(ctx context.Context, principal auth.Principal, workspaceID, siteID, sessionID string) (Site, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return Site{}, err
	}
	session, err := s.GetDesignSession(ctx, principal, workspaceID, sessionID)
	if err != nil {
		return Site{}, err
	}
	if session.Status != "active" {
		return Site{}, ErrConflict
	}
	current, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return Site{}, err
	}
	rawPages := json.RawMessage(session.SessionConfig)
	updated, err := s.UpdateSite(ctx, principal, workspaceID, siteID, current.ETag, UpdateSiteInput{
		PagesConfig: &rawPages,
	})
	if err != nil {
		return Site{}, err
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE site.design_sessions SET status = 'applied', updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, sessionID); err != nil {
		return Site{}, fmt.Errorf("close design session: %w", err)
	}
	return updated, nil
}

// DiscardDesignSession drops the sandbox (放弃迭代零残留).
func (s Service) DiscardDesignSession(ctx context.Context, principal auth.Principal, workspaceID, sessionID string) error {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE site.design_sessions SET status = 'discarded', updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
	`, principal.OrganizationID, sessionID)
	if err != nil {
		return fmt.Errorf("discard design session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSiteNotFound
	}
	return nil
}

// PagesConfigFork 归一会话 fork 文档：v2 优先；旧站（无 v2 文档）以默认
// 配置起步，hero 文案取站点名。
func PagesConfigFork(pagesConfig, legacyHomepage json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(pagesConfig))) > 0 {
		return pagesConfig
	}
	raw, err := json.Marshal(DefaultPagesConfig())
	if err != nil {
		return []byte("{}")
	}
	_ = legacyHomepage
	return raw
}
