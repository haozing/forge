package httpapi

// external_links_routes.go — 模型公开目录页的根路径路由与公开提交端点
// （外链板块 v2）。仅单知识库根站点部署（DELIVERY_ROOT_SITE_SLUG）注册：
// 路由挂在站点根下（/external-links…），页面服务走 delivery 服务的目录
// builder；提交端点把表单落成 external_site 模型的草稿记录进治理链。

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/delivery"
)

// externalModelKey 是外链站点模型键；提交端点按它解析组织内的模型 id。
const externalModelKey = "external_site"

// submitExternalLink 落地 /external-links/submit 的 JS-free 表单：
// 蜜罐先行（机器人填隐藏字段 → 假成功 303，不落库）→ 公共限流预算 →
// 字段校验 → 以站点创建者身份创建 external_site 草稿记录（origin
// external_links_submission 进 raw_inputs）→ 303 回目录页成功锚点。
func submitExternalLink(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if deps.RootSiteSlug == "" || r.Method != http.MethodPost {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/external-links?error=form", http.StatusSeeOther)
			return
		}
		// 蜜罐：隐藏字段被填写即判定为机器人，直接回成功页（不落库、
		// 不暴露判定逻辑）。
		if strings.TrimSpace(r.PostFormValue("website_fill")) != "" {
			http.Redirect(w, r, "/external-links?submitted=1", http.StatusSeeOther)
			return
		}
		trim := func(key string, max int) string {
			value := strings.TrimSpace(r.PostFormValue(key))
			if len(value) > max {
				value = value[:max]
			}
			return value
		}
		siteName := trim("site_name", 120)
		siteURL := trim("site_url", 500)
		category := trim("category", 64)
		description := trim("description", 400)
		testedDate := trim("tested_date", 10)
		agentSkill := trim("agent_skill", 8000)
		submitterName := trim("submitter_site_name", 120)
		submitterURL := trim("submitter_site_url", 500)
		contactEmail := trim("contact_email", 200)

		// 公共读面共享的 IP 预算同样约束提交（滥用第一道闸）。
		if deps.PublicSites != nil {
			if err := deps.PublicSites.AllowPublic(r.Context(),
				effectiveClientAddr(r, deps.TrustedProxyCIDRs)); err != nil {
				http.Redirect(w, r, "/external-links?error=rate", http.StatusSeeOther)
				return
			}
		}
		// 必填与枚举校验（非法直接回退，不落库）。
		if siteName == "" || siteURL == "" || description == "" || contactEmail == "" ||
			!delivery.DirectoryCategoryValid(category) {
			http.Redirect(w, r, "/external-links?error=fields", http.StatusSeeOther)
			return
		}

		facts, err := deps.PublicSites.SiteFacts(r.Context(), deps.RootSiteSlug)
		if err != nil {
			http.Redirect(w, r, "/external-links?error=save", http.StatusSeeOther)
			return
		}
		// 解析 external_site 模型 id（组织级优先）与站点创建者（草稿
		// created_by 落点：公开提交无登录身份，以站点所有者名义入库）。
		var modelID string
		if err := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT rm.id::text FROM model.resource_models rm
			WHERE rm.organization_id = $1::uuid AND rm.model_key = $2 AND rm.status = 'active'
			ORDER BY rm.workspace_id NULLS FIRST LIMIT 1
		`, facts.Site.OrganizationID, externalModelKey).Scan(&modelID); err != nil {
			http.Redirect(w, r, "/external-links?error=save", http.StatusSeeOther)
			return
		}
		var createdBy string
		if err := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT created_by::text FROM site.public_sites WHERE slug = $1
		`, deps.RootSiteSlug).Scan(&createdBy); err != nil {
			http.Redirect(w, r, "/external-links?error=save", http.StatusSeeOther)
			return
		}

		// 以站点创建者身份创建草稿记录（治理链起点：确认+发布后才上站）。
		principal := auth.Principal{
			UserType:       auth.UserTypeMember,
			OrganizationID: facts.Site.OrganizationID,
			UserID:         createdBy,
		}
		fields := map[string]any{
			"site_name":     siteName,
			"site_url":      siteURL,
			"category":      category,
			"description":   description,
			"contact_email": contactEmail,
		}
		if testedDate != "" {
			fields["tested_date"] = testedDate
		}
		if agentSkill != "" {
			fields["agent_skill"] = agentSkill
		}
		if submitterName != "" {
			fields["submitter_site_name"] = submitterName
		}
		if submitterURL != "" {
			fields["submitter_site_url"] = submitterURL
		}
		if _, err := deps.MemberAssetService.Create(r.Context(), principal,
			facts.Site.WorkspaceID,
			fmt.Sprintf("external-submission-%d", time.Now().UnixNano()),
			asset.MemberAssetInput{
				ResourceModelID: modelID,
				Title:           &siteName,
				Fields:          fields,
				Source:          map[string]any{"channel": "external_links_submission"},
			}); err != nil {
			http.Redirect(w, r, "/external-links?error=save", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/external-links?submitted=1", http.StatusSeeOther)
	}
}

// rootSiteDirectoryPages serves the directory GET routes:
//   - /external-links                        总目录（?page=N / ?submitted=1）
//   - /external-links/submission-guidelines  规则说明页
//   - /external-links/{category}             分类子页（枚举内）
//
// 其余路径渲染目录 404 页（不落全局 404，保持站点壳层）。
func rootSiteDirectoryPages(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		addr := effectiveClientAddr(r, deps.TrustedProxyCIDRs)
		principal := publicVisitorPrincipal(r, deps)
		baseURL := deps.deliveryBaseURL(r)
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/external-links"), "/")

		switch {
		case path == "":
			submitted := r.URL.Query().Get("submitted") == "1"
			page := pageParam(r)
			resp, err := deps.Delivery.ExternalLinks(r.Context(), addr, principal, baseURL, page, submitted && page == 1)
			if err != nil {
				writeDeliveryError(w, r, service, err)
				return
			}
			writeDeliveryPage(w, r, service, resp)
		case path == "submission-guidelines":
			resp, err := deps.Delivery.SubmissionGuidelines(r.Context(), addr, principal, baseURL)
			if err != nil {
				writeDeliveryError(w, r, service, err)
				return
			}
			writeDeliveryPage(w, r, service, resp)
		case !strings.Contains(path, "/"):
			category := path
			if !delivery.DirectoryCategoryValid(category) {
				writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
				return
			}
			resp, err := deps.Delivery.ExternalLinksCategory(r.Context(), addr, principal, baseURL, category, pageParam(r))
			if err != nil {
				writeDeliveryError(w, r, service, err)
				return
			}
			writeDeliveryPage(w, r, service, resp)
		default:
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
		}
	}
}
