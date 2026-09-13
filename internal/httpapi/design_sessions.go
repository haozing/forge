package httpapi

// resource_model_design_session.go — AI 设计会话 HTTP 面（站点方案 D/§10）。
// POST   /api/workspaces/{ws}/sites/{siteId}/design-sessions            开始（fork 沙盒）
// GET    /api/workspaces/{ws}/sites/{siteId}/design-sessions/{id}       读会话
// PATCH  /api/workspaces/{ws}/sites/{siteId}/design-sessions/{id}       apply_patch（写沙盒）
// DELETE /api/workspaces/{ws}/sites/{siteId}/design-sessions/{id}       放弃
// POST   .../apply                                                       应用（人；site.design）
// POST   .../observe                                                     观察当前沙盒（截图或结构化降级）

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/screenshot"
)

func designSessionEndpoints(deps Dependencies) (start, get, patch, apply func(w http.ResponseWriter, r *http.Request)) {
	return func(w http.ResponseWriter, r *http.Request) {
			principal, ok := requireMemberSession(w, r, deps)
			if !ok {
				return
			}
			workspaceID := r.PathValue("workspaceId")
			siteID := r.PathValue("siteId")
			if !requirePathUUID(w, workspaceID, siteID) {
				return
			}
			session, err := deps.Sites.StartDesignSession(r.Context(), principal, workspaceID, siteID)
			if err != nil {
				SiteError(w, err, "design_session_failed")
				return
			}
			writeJSON(w, http.StatusCreated, session)
		},
		func(w http.ResponseWriter, r *http.Request) {
			principal, ok := requireMemberSession(w, r, deps)
			if !ok {
				return
			}
			workspaceID := r.PathValue("workspaceId")
			if !requirePathUUID(w, workspaceID) {
				return
			}
			session, err := deps.Sites.GetDesignSession(r.Context(), principal, workspaceID, r.PathValue("sessionId"))
			if err != nil {
				SiteError(w, err, "design_session_failed")
				return
			}
			writeJSON(w, http.StatusOK, session)
		},
		func(w http.ResponseWriter, r *http.Request) {
			principal, ok := requireMemberSession(w, r, deps)
			if !ok {
				return
			}
			workspaceID := r.PathValue("workspaceId")
			if !requirePathUUID(w, workspaceID) {
				return
			}
			var input struct {
				PagesConfig json.RawMessage `json:"pages_config"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			session, err := deps.Sites.ApplyDesignPatch(r.Context(), principal, workspaceID, r.PathValue("sessionId"), input.PagesConfig)
			if err != nil {
				SiteError(w, err, "design_session_patch_failed")
				return
			}
			writeJSON(w, http.StatusOK, session)
		},
		func(w http.ResponseWriter, r *http.Request) {
			principal, ok := requireMemberSession(w, r, deps)
			if !ok {
				return
			}
			workspaceID := r.PathValue("workspaceId")
			siteID := r.PathValue("siteId")
			if !requirePathUUID(w, workspaceID, siteID) {
				return
			}
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
				return
			}
			updated, err := deps.Sites.ApplyDesignSession(r.Context(), principal, workspaceID, siteID, r.PathValue("sessionId"))
			if err != nil {
				SiteError(w, err, "design_session_apply_failed")
				return
			}
			writeData(w, r, http.StatusOK, updated)
		}
}

// observeDesignSession renders the sandbox's home page and captures
// desktop/mobile screenshots (RENDERER_ENABLED + host chrome), degrading to
// structured observation (config + validation + block stats) when the
// renderer is off. 截图以 base64 PNG 返回，供多模态模型与设计面板共用。
func observeDesignSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		sessionID := r.PathValue("sessionId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		session, err := deps.Sites.GetDesignSession(r.Context(), principal, workspaceID, sessionID)
		if err != nil {
			SiteError(w, err, "design_session_failed")
			return
		}

		observation := map[string]any{
			"session_id":  session.ID,
			"status":      session.Status,
			"screenshots": []any{},
			"issues":      []any{},
		}

		if deps.Delivery != nil && deps.DeliveryPublicBaseURL != "" {
			page, renderErr := deps.Delivery.RenderPreview(r.Context(), principal, workspaceID, session.SiteID, delivery.PreviewInput{
				PagesConfig: session.SessionConfig,
				Page:        "home",
				BaseURL:     deps.DeliveryPublicBaseURL,
			})
			if renderErr == nil {
				enabled := os.Getenv("RENDERER_ENABLED") == "true"
				pngs, capErr := screenshot.Capture(r.Context(), enabled, string(page.Body), screenshot.Viewports(), 20*time.Second)
				if capErr != nil {
					observation["screenshots_error"] = capErr.Error()
				} else {
					shots := []map[string]string{}
					for name, png := range pngs {
						shots = append(shots, map[string]string{
							"name": name,
							"data": base64.StdEncoding.EncodeToString(png),
						})
					}
					observation["screenshots"] = shots
				}
			} else {
				observation["render_error"] = renderErr.Error()
			}
		}

		writeJSON(w, http.StatusOK, observation)
	}
}
