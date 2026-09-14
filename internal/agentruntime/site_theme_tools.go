package agentruntime

// site_theme_tools.go — §7.2 主题设计工具集的 handler 构造。工具落在
// react runs 通道，全部要求 site.design 且受发起人/agent 双权限交集约束
//（allowed() 地板角色）；发布仍走 site.lifecycle（human_only），agent
// 只能到草稿/沙盒。工具声明（名称/参数/risk）在 tools/builtins.go。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/theme"
)

// maxRenderHTMLBytes 是 render_html 返回给模型的 HTML 上限（超长截断，
// 模型自检以结构为主，不需要全量字节）。
const maxRenderHTMLBytes = 48 << 10

// sessionFilesJSON 把会话 files（json.RawMessage）规范成 JSON（空集兜底 {}）。
func sessionFilesJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

// themeFilesMap 把 JSON 文件集解码为引擎入参（槽位 → 源码）。
func themeFilesMap(raw json.RawMessage) (map[string]string, error) {
	files := map[string]string{}
	if err := json.Unmarshal(sessionFilesJSON(raw), &files); err != nil {
		return nil, fmt.Errorf("theme files decode: %w", err)
	}
	return files, nil
}

// applySiteThemeTools 在 Sites 域已接线时填充 §7.2 的七个工具 handler；
// 每个工具先过 allowed("site.design") 交集再进域逻辑。
func (f DomainToolFactory) applySiteThemeTools(handlers *runtimetools.BuiltinHandlers, scope ReActToolScope, principal auth.Principal, allowed func(ctx context.Context, action string) ([]string, error)) {
	if f.Sites == nil {
		return
	}
	// principal 必须复用 Build 里已补载 Capabilities 的那个：站点域
	// Require 的能力门会校验 principal.Capabilities。
	sites := f.Sites
	workspaceID := scope.WorkspaceID
	gate := func(handler runtimetools.JSONHandler) runtimetools.JSONHandler {
		return func(ctx context.Context, arguments map[string]any) (any, error) {
			if _, err := allowed(ctx, "site.design"); err != nil {
				return nil, err
			}
			return handler(ctx, arguments)
		}
	}
	// openSession 复用站点当前 open 会话（无则 fork）——write/render/
	// preview/validate 与工作台「拉取 agent 修改」读同一行沙盒。
	openSession := func(ctx context.Context, siteID string) (site.DesignSession, error) {
		return sites.StartDesignSession(ctx, principal, workspaceID, siteID)
	}

	handlers.ReadTheme = gate(func(ctx context.Context, arguments map[string]any) (any, error) {
		session, err := openSession(ctx, stringValue(arguments["site_id"]))
		if err != nil {
			return nil, err
		}
		files, err := themeFilesMap(session.Files)
		if err != nil {
			return nil, err
		}
		if slot := stringValue(arguments["slot"]); slot != "" {
			return map[string]any{"session_id": session.ID, "slot": slot, "content": files[slot]}, nil
		}
		return map[string]any{"session_id": session.ID, "files": files}, nil
	})

	handlers.WriteTheme = gate(func(ctx context.Context, arguments map[string]any) (any, error) {
		siteID := stringValue(arguments["site_id"])
		slot := stringValue(arguments["slot"])
		content := stringValue(arguments["content"])
		if slot == "" || content == "" {
			return nil, errors.New("slot and content are required")
		}
		session, err := openSession(ctx, siteID)
		if err != nil {
			return nil, err
		}
		files, err := themeFilesMap(session.Files)
		if err != nil {
			return nil, err
		}
		files[slot] = content
		encoded, err := json.Marshal(files)
		if err != nil {
			return nil, err
		}
		saved, err := sites.SaveDesignSessionFiles(ctx, principal, workspaceID, session.ID, encoded)
		if err != nil {
			return nil, err
		}
		// 写后即编译+扫描（§7.2：错误即回显，agent 当轮自修复；不阻断保存）。
		row, err := sites.GetSite(ctx, principal, workspaceID, siteID)
		if err != nil {
			return nil, err
		}
		report := map[string]any{"session_id": saved.ID, "slot": slot, "saved": true}
		compiled, cerr := theme.Compile(files, theme.Options{SiteSlug: row.Slug})
		if cerr != nil {
			var ce *theme.CompileError
			if errors.As(cerr, &ce) {
				report["ok"] = false
				report["issues"] = ce.Problems
				return report, nil
			}
			return nil, cerr
		}
		report["ok"] = true
		report["revision"] = compiled.Revision()
		return report, nil
	})

	handlers.RenderHTML = gate(func(ctx context.Context, arguments map[string]any) (any, error) {
		if f.Delivery == nil {
			return nil, errors.New("render_html is unavailable")
		}
		siteID := stringValue(arguments["site_id"])
		session, err := openSession(ctx, siteID)
		if err != nil {
			return nil, err
		}
		slot := stringValue(arguments["slot"])
		resp, err := f.Delivery.RenderPreview(ctx, principal, workspaceID, siteID, delivery.PreviewInput{
			Files:       sessionFilesJSON(session.Files),
			Slot:        slot,
			DisplayPath: stringValue(arguments["display_path"]),
		})
		if err != nil {
			return nil, err
		}
		body := string(resp.Body)
		truncated := false
		if len(body) > maxRenderHTMLBytes {
			body = body[:maxRenderHTMLBytes]
			truncated = true
		}
		return map[string]any{"session_id": session.ID, "slot": slot, "html": body, "truncated": truncated}, nil
	})

	handlers.PreviewLink = gate(func(ctx context.Context, arguments map[string]any) (any, error) {
		siteID := stringValue(arguments["site_id"])
		session, err := openSession(ctx, siteID)
		if err != nil {
			return nil, err
		}
		slot := stringValue(arguments["slot"])
		if slot == "" {
			slot = theme.SlotHome
		}
		token, err := sites.IssuePreviewToken(ctx, principal, workspaceID, siteID, session.ID, slot)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"session_id": session.ID, "slot": slot, "token": token, "expires_in_seconds": 60,
			// 相对 API 根的直出路径（管理台 iframe 用；一次性，消费即失效）。
			"url_path": fmt.Sprintf("/api/workspaces/%s/sites/%s/design-sessions/%s/preview?slot=%s&token=%s",
				workspaceID, siteID, session.ID, slot, token),
		}, nil
	})

	handlers.ValidateTheme = gate(func(ctx context.Context, arguments map[string]any) (any, error) {
		siteID := stringValue(arguments["site_id"])
		session, err := openSession(ctx, siteID)
		if err != nil {
			return nil, err
		}
		files, err := themeFilesMap(session.Files)
		if err != nil {
			return nil, err
		}
		row, err := sites.GetSite(ctx, principal, workspaceID, siteID)
		if err != nil {
			return nil, err
		}
		present := make([]string, 0, len(files))
		for key := range files {
			present = append(present, key)
		}
		report := map[string]any{"session_id": session.ID, "slots_present": present}
		compiled, cerr := theme.Compile(files, theme.Options{SiteSlug: row.Slug})
		if cerr != nil {
			var ce *theme.CompileError
			if errors.As(cerr, &ce) {
				report["ok"] = false
				report["issues"] = ce.Problems
				return report, nil
			}
			return nil, cerr
		}
		report["ok"] = true
		report["revision"] = compiled.Revision()
		return report, nil
	})

	handlers.UpdateSite = gate(func(ctx context.Context, arguments map[string]any) (any, error) {
		siteID := stringValue(arguments["site_id"])
		row, err := sites.GetSite(ctx, principal, workspaceID, siteID)
		if err != nil {
			return nil, err
		}
		input := site.UpdateSiteInput{}
		if value, ok := arguments["name"].(string); ok && value != "" {
			input.Name = &value
		}
		if value, ok := arguments["description"].(string); ok {
			input.Description = &value
		}
		if input.Name == nil && input.Description == nil {
			return nil, errors.New("name or description is required")
		}
		return sites.UpdateSite(ctx, principal, workspaceID, siteID, fmt.Sprintf("%d", row.Revision), input)
	})

	handlers.ManagePages = gate(func(ctx context.Context, arguments map[string]any) (any, error) {
		siteID := stringValue(arguments["site_id"])
		switch stringValue(arguments["action"]) {
		case "create":
			slug := stringValue(arguments["slug"])
			input := site.CustomPageInput{Slug: &slug}
			if value, ok := arguments["title"].(string); ok {
				input.Title = &value
			}
			if value, ok := arguments["body_markdown"].(string); ok {
				input.BodyMarkdown = &value
			}
			if value, ok := arguments["seo_description"].(string); ok {
				input.SEODescription = &value
			}
			return sites.CreateSitePage(ctx, principal, workspaceID, siteID, input)
		case "update":
			input := site.CustomPageInput{}
			if value, ok := arguments["title"].(string); ok {
				input.Title = &value
			}
			if value, ok := arguments["body_markdown"].(string); ok {
				input.BodyMarkdown = &value
			}
			if value, ok := arguments["seo_description"].(string); ok {
				input.SEODescription = &value
			}
			if value, ok := arguments["nav_hidden"].(bool); ok {
				input.NavHidden = &value
			}
			if value, ok := arguments["nav_order"].(float64); ok {
				order := int(value)
				input.NavOrder = &order
			}
			return sites.UpdateSitePage(ctx, principal, workspaceID, siteID, stringValue(arguments["page_id"]), input)
		case "delete":
			if err := sites.DeleteSitePage(ctx, principal, workspaceID, siteID, stringValue(arguments["page_id"])); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true}, nil
		default:
			return nil, errors.New("action must be create | update | delete")
		}
	})
}
