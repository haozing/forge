package httpapi

// public_chat_handlers.go — 公开站 AI 问答（/ask，2026-09-21 设计）：
// 页面壳 + SSE 流式问答 + 额度查询 + chat 岛脚本。成员会话必需。

import (
	"errors"
	"net/http"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/site"
)

// deliverySiteAsk serves /sites/{slug}/ask: the chat shell for members,
// the login CTA for anonymous visitors.
func deliverySiteAsk(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		principal, _ := deps.SessionService.Authenticate(r.Context(), r)
		page, err := service.AskPage(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			principal, slug, deps.deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// publicSiteChat serves POST /api/public/sites/{slug}/chat: the member-only
// quota-gated RAG chat stream (SSE frames over the JSON face).
func publicSiteChat(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeError(w, http.StatusNotFound, "site_not_found")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil || principal.UserType != auth.UserTypeMember {
			writeError(w, http.StatusUnauthorized, "login_required")
			return
		}
		var input struct {
			SessionID string `json:"session_id"`
			Message   string `json:"message"`
		}
		if !decodeBody(w, r, &input, 8*1024) {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		writeFrame := func(frame delivery.ChatStreamFrame) error {
			return writeSSE(w, flusher, "message", frame)
		}
		if err := service.PublicChatStream(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			principal, slug, input, writeFrame); err != nil {
			_ = writeSSE(w, flusher, "message", delivery.ChatStreamFrame{Type: "error", Message: sseErrorMessage(err)})
		}
	}
}

// publicSiteChatQuota serves GET /api/public/sites/{slug}/chat/quota.
func publicSiteChatQuota(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeError(w, http.StatusNotFound, "site_not_found")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil || principal.UserType != auth.UserTypeMember {
			writeError(w, http.StatusUnauthorized, "login_required")
			return
		}
		page, err := service.ChatQuota(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			principal, slug, deps.deliveryBaseURL(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "chat_quota_failed")
			return
		}
		w.Header().Set("Content-Type", page.ContentType)
		w.Header().Set("Cache-Control", page.CacheControl)
		w.WriteHeader(page.Status)
		_, _ = w.Write(page.Body)
	}
}

// deliveryChatScript serves the embedded chat island script.
func deliveryChatScript(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Delivery == nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		page := deps.Delivery.ChatScript()
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(page.Status)
		_, _ = w.Write(page.Body)
	}
}

func sseErrorMessage(err error) string {
	switch {
	case errors.Is(err, delivery.ErrChatQuotaExhausted):
		return "今日额度已用完，明日恢复。"
	case errors.Is(err, delivery.ErrChatInvalidMessage):
		return "消息不合法。"
	case errors.Is(err, delivery.ErrChatLoginRequired):
		return "请先登录。"
	default:
		return "服务暂时不可用，请稍后重试。"
	}
}
