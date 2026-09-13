package screenshot

// screenshot_integration_test.go — 真 chrome 截图验证（D/§10.2）。
// 运行：RENDERER_ENABLED=true go test ./internal/screenshot/ -v
// 宿主机需装有 Chrome/Edge（chromedp 自动探测）；容器内由镜像预装。
// 未开启时全部跳过——设计会话走结构化观察降级路径，不失败。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const verifyHTML = `<!DOCTYPE html>
<html lang="zh"><head><meta charset="utf-8"><title>验证页</title>
<style>body{font-family:system-ui;margin:0;padding:48px;background:#faf9f7;color:#1b1f1c}
.hero{font-size:32px;font-weight:700;margin-bottom:12px}
.card{border:1px solid #ddd;border-radius:8px;padding:16px;margin-bottom:12px;max-width:560px}</style>
</head><body>
<div class="hero">验证站</div>
<div class="card"><h3>沙丘2 影评</h3><p>评分 9 · 2026-09</p></div>
<div class="card"><h3>镜头语言入门</h3><p>评分 8 · 2026-09</p></div>
</body></html>`

func TestCaptureRealChrome(t *testing.T) {
	if os.Getenv("RENDERER_ENABLED") != "true" {
		t.Skip("RENDERER_ENABLED != true：跳过真 chrome 截图验证")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pngs, err := Capture(ctx, true, verifyHTML, Viewports(), 20*time.Second)
	if err != nil {
		// chrome 缺失也归为降级路径，不算失败——调用方按 ErrRendererDisabled 处理
		if strings.Contains(err.Error(), "executable file not found") ||
			strings.Contains(err.Error(), ErrRendererDisabled.Error()) {
			t.Skipf("本机无 chrome，降级为结构化观察: %v", err)
		}
		t.Fatalf("capture failed: %v", err)
	}
	for name, png := range pngs {
		if len(png) < 1024 {
			t.Errorf("viewport %s: 截图过小（%d bytes），疑似空图", name, len(png))
		}
		if os.Getenv("SCREENSHOT_OUT_DIR") != "" {
			out := filepath.Join(os.Getenv("SCREENSHOT_OUT_DIR"), "verify-"+name+".png")
			if writeErr := os.WriteFile(out, png, 0o644); writeErr != nil {
				t.Logf("write %s: %v", out, writeErr)
			} else {
				t.Logf("screenshot written: %s (%d bytes)", out, len(png))
			}
		}
	}
	if len(pngs["desktop"]) == 0 || len(pngs["mobile"]) == 0 {
		t.Fatal("desktop/mobile 两档截图必须齐备")
	}
}

func TestCaptureDisabledDegradation(t *testing.T) {
	_, err := Capture(context.Background(), false, verifyHTML, nil, 0)
	if err == nil || err.Error() != ErrRendererDisabled.Error() {
		t.Fatalf("disabled renderer must return ErrRendererDisabled, got %v", err)
	}
}

func TestSanitizeForRenderStripsExternalResources(t *testing.T) {
	html := `<img src="https://evil.example/x.png"><a href="https://ok.example/">l</a>`
	out := SanitizeForRender(html)
	if strings.Contains(out, "https://evil") {
		t.Fatalf("external src must be blocked, got %s", out)
	}
}
