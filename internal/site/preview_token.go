package site

// preview_token.go — 一次性预览 token 签发（§7.2 preview_link 工具与
// httpapi preview-link 端点共用的落点）。消费校验在 httpapi（绑定会话/
// 站点/槽位/过期/单次）。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
)

// previewTokenSign 计算一次性预览 token 的 HMAC 摘要输入/密钥（与消费端
// previewDesignSession 完全一致：secret = QueryHashSecret + "|preview"）。
func previewTokenSign(secret, sessionID, siteID, slot, nonce string) []byte {
	mac := hmac.New(sha256.New, []byte(secret+"|preview"))
	mac.Write([]byte(sessionID + "|" + siteID + "|" + slot + "|" + nonce))
	return mac.Sum(nil)
}

// IssuePreviewToken 为会话沙盒签发 60 秒一次性预览 token：HMAC 摘要入库，
// 返回 nonce 形态的 token。鉴权由调用方完成（HTTP 面为 site.read，agent
// 工具走 site.design 交集）；PreviewHashSecret 为空 = 未配置，拒绝签发。
func (s Service) IssuePreviewToken(ctx context.Context, principal auth.Principal, workspaceID, siteID, sessionID, slot string) (string, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return "", err
	}
	if s.PreviewHashSecret == "" {
		return "", fmt.Errorf("preview token signing is not configured")
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(nonceBytes)
	if _, err := s.Store.Pool.Exec(ctx, `
		INSERT INTO site.preview_tokens (digest, organization_id, session_id, site_id, slot, expires_at)
		SELECT $1::bytea, organization_id, id, $3::uuid, $4, now() + interval '60 seconds'
		FROM site.design_sessions WHERE id = $2::uuid
	`, previewTokenSign(s.PreviewHashSecret, sessionID, siteID, slot, nonce), sessionID, siteID, slot); err != nil {
		return "", fmt.Errorf("issue preview token: %w", err)
	}
	return nonce, nil
}
