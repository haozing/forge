package site

// activity.go — 站点动态时间线（内容治理四件套 F3-D2，对应 llm_wiki 的
// log.md）：合并 audit.audit_log（治理动作：配置/主题/分类/收录/自定义页）
// 与 asset.asset_publications（发布史）为单一站点视角时间线。只读，内部
// 消费面——公开站更新记录块继续只吃 asset_publications，audit 元数据
// 绝不外溢。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
)

// ActivityItem 是时间线上的单条事实。Kind 区分来源：audit（治理动作）/
// publish（发布史）。ActorType 用于前端 Agent 徽章。
type ActivityItem struct {
	Kind       string          `json:"kind"`
	Time       time.Time       `json:"time"`
	Action     string          `json:"action"`
	ActorName  string          `json:"actor_name"`
	ActorType  string          `json:"actor_type"`
	ResourceID string          `json:"resource_id,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	// publish 专属字段。
	AssetID            string `json:"asset_id,omitempty"`
	VersionNo          int    `json:"version_no,omitempty"`
	Origin             string `json:"origin,omitempty"`
	ConfirmationStatus string `json:"confirmation_status,omitempty"`
	ChangeNote         string `json:"change_note,omitempty"`
}

// SiteActivity returns the merged site timeline (newest first, capped).
func (s *Service) SiteActivity(ctx context.Context, principal auth.Principal, workspaceID, siteID string, limit int) ([]ActivityItem, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return nil, err
	}
	if _, err := s.GetSite(ctx, principal, workspaceID, siteID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items, err := s.gatherActivity(ctx, principal.OrganizationID, workspaceID, siteID, limit)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []ActivityItem{}
	}
	return items, nil
}

func (s *Service) gatherActivity(ctx context.Context, organizationID, workspaceID, siteID string, limit int) ([]ActivityItem, error) {
	items := []ActivityItem{}

	// 治理动作：站点域审计（recordSiteAudit 全部落 resource_type='site'、
	// resource_id=siteID）。actor 关联 users 取展示名与类型（agent 徽章）。
	auditRows, err := s.Store.Pool.Query(ctx, `
		SELECT a.action, a.created_at, COALESCE(a.resource_id::text, ''),
		       COALESCE(a.metadata, '{}'::jsonb), COALESCE(u.display_name, '未知'),
		       COALESCE(u.user_type, 'member')
		FROM audit.audit_log a
		LEFT JOIN identity.users u ON u.id = a.actor_user_id
		WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
		  AND a.resource_type = 'site' AND a.resource_id = $3::uuid
		ORDER BY a.created_at DESC
		LIMIT $4::int
	`, organizationID, workspaceID, siteID, limit)
	if err != nil {
		return nil, fmt.Errorf("activity audit query: %w", err)
	}
	defer auditRows.Close()
	for auditRows.Next() {
		var item ActivityItem
		item.Kind = "audit"
		if err := auditRows.Scan(&item.Action, &item.Time, &item.ResourceID,
			&item.Metadata, &item.ActorName, &item.ActorType); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := auditRows.Err(); err != nil {
		return nil, err
	}

	// 发布史：限定本站当前收录（slug 当前行）的资产。
	publishRows, err := s.Store.Pool.Query(ctx, `
		SELECT p.published_at, 'asset.publish', COALESCE(u.display_name, '未知'),
		       COALESCE(u.user_type, 'member'), p.asset_id::text, p.version_no,
		       p.origin, p.confirmation_status, COALESCE(p.change_note, '')
		FROM asset.asset_publications p
		JOIN site.site_slugs sl
		  ON sl.organization_id = p.organization_id AND sl.asset_id = p.asset_id
		 AND sl.site_id = $3::uuid AND sl.is_current
		LEFT JOIN identity.users u ON u.id = p.published_by
		WHERE p.organization_id = $1::uuid AND p.workspace_id = $2::uuid
		ORDER BY p.published_at DESC
		LIMIT $4::int
	`, organizationID, workspaceID, siteID, limit)
	if err != nil {
		return nil, fmt.Errorf("activity publish query: %w", err)
	}
	defer publishRows.Close()
	for publishRows.Next() {
		var item ActivityItem
		item.Kind = "publish"
		if err := publishRows.Scan(&item.Time, &item.Action, &item.ActorName, &item.ActorType,
			&item.AssetID, &item.VersionNo, &item.Origin, &item.ConfirmationStatus,
			&item.ChangeNote); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := publishRows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].Time.After(items[j].Time) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
