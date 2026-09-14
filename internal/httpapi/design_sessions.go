package httpapi

// design_sessions.go v2 — AI 设计会话 HTTP 面（主题化与 AI 设计重构）。
// POST   /api/workspaces/{ws}/sites/{siteId}/design-sessions            开始（fork 沙盒）
// GET    /api/workspaces/{ws}/sites/{siteId}/design-sessions/{id}       读会话（含 files/issues）
// PATCH  /api/workspaces/{ws}/sites/{siteId}/design-sessions/{id}       写沙盒文件集（write_theme）
// DELETE /api/workspaces/{ws}/sites/{siteId}/design-sessions/{id}       放弃
// POST   .../apply                                                       应用到站点草稿（site.design）
// POST   .../preview-link                                                签发一次性预览 token
// GET    .../preview?slot=&token=                                        沙盒渲染 HTML 直出（一次性）

import (
	crand "crypto/rand"
	"log"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/delivery"

	"github.com/jackc/pgx/v5"
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
			writeData(w, r, http.StatusCreated, session)
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
			writeData(w, r, http.StatusOK, session)
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
				Files json.RawMessage `json:"files"`
			}
			if !decodeBody(w, r, &input, 128*1024) {
				return
			}
			session, err := deps.Sites.SaveDesignSessionFiles(r.Context(), principal, workspaceID, r.PathValue("sessionId"), input.Files)
			if err != nil {
				SiteError(w, err, "design_session_patch_failed")
				return
			}
			writeData(w, r, http.StatusOK, session)
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
				log.Printf("design session apply failed: %v", err)
				SiteError(w, err, "design_session_apply_failed")
				return
			}
			writeData(w, r, http.StatusOK, updated)
		}
}

// previewTokenSign 已下沉 site.Service.IssuePreviewToken（preview_token.go）；
// 消费端校验仍在此处（deps.QueryHashSecret）。
func previewTokenSign(deps Dependencies, sessionID, siteID, slot string) (nonce string, digest []byte, err error) {
	nonceBytes := make([]byte, 16)
	if _, err = crand.Read(nonceBytes); err != nil {
		return "", nil, err
	}
	nonce = hex.EncodeToString(nonceBytes)
	mac := hmac.New(sha256.New, []byte(deps.QueryHashSecret + "|preview"))
	mac.Write([]byte(sessionID + "|" + siteID + "|" + slot + "|" + nonce))
	return nonce, mac.Sum(nil), nil
}

// previewLinkDesignSession 签发一次性预览 token。
func previewLinkDesignSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		sessionID := r.PathValue("sessionId")
		if !requirePathUUID(w, workspaceID, siteID, sessionID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
			return
		}
		slot := r.URL.Query().Get("slot")
		if slot == "" {
			slot = "home"
		}
		token, err := deps.Sites.IssuePreviewToken(r.Context(), principal, workspaceID, siteID, sessionID, slot)
		if err != nil {
			SiteError(w, err, "preview_link_failed")
			return
		}
		writeData(w, r, http.StatusOK, map[string]string{"token": token, "slot": slot})
	}
}

// previewDesignSession 直出沙盒渲染 HTML（一次性 token 校验后）。
func previewDesignSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		sessionID := r.PathValue("sessionId")
		slot := r.URL.Query().Get("slot")
		if slot == "" {
			slot = "home"
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			writeError(w, http.StatusUnauthorized, "preview_token_required")
			return
		}
		mac := hmac.New(sha256.New, []byte(deps.QueryHashSecret+"|preview"))
		mac.Write([]byte(sessionID + "|" + siteID + "|" + slot + "|" + token))
		digest := mac.Sum(nil)

		// 一次性消费 + 校验（绑定会话/站点/槽位/过期）。
		var sessionRef string
		err := deps.Store.Pool.QueryRow(r.Context(), `
			UPDATE site.preview_tokens SET consumed_at = now()
			WHERE digest = $1::bytea AND session_id = $2::uuid AND site_id = $3::uuid
			  AND slot = $4 AND consumed_at IS NULL AND expires_at > now()
			RETURNING session_id::text
		`, digest, sessionID, siteID, slot).Scan(&sessionRef)
		if errors.Is(err, pgx.ErrNoRows) {
			log.Printf("preview token consumed 0 rows: session=%s slot=%s token=%s", sessionID, slot, token)
			writeError(w, http.StatusUnauthorized, "preview_token_invalid")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}

		// 渲染沙盒（RenderPreview 内部完成 member 会话授权与取数）。
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		// 沙盒文件集 = 会话的 files；缺失（理论不可达：token 消费已绑定
		// 会话行）时回落 published 主题。
		var files json.RawMessage
		_ = deps.Store.Pool.QueryRow(r.Context(), `
			SELECT files FROM site.design_sessions
			WHERE id = $1::uuid AND site_id = $2::uuid
		`, sessionID, siteID).Scan(&files)
		input := delivery.PreviewInput{Slot: slot, Files: files}
		resp, err := service.RenderPreview(r.Context(), principal, workspaceID, siteID, input)
		if err != nil {
			SiteError(w, err, "preview_failed")
			return
		}
		// 预览专属 CSP：允许被管理端 iframe 嵌入（交付页是 frame-ancestors 'none'）。
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'self'")
		w.Header().Set("Content-Type", resp.ContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	}
}
