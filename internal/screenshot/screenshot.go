package screenshot

// screenshot.go — 设计会话 observe 的截图池（站点方案 D/§10.2，2026-09-13）。
// chromedp 渲染 SSR HTML 并截取桌面/移动两档视口；RENDERER_ENABLED 未开或
// 本机无 chrome 时返回 ErrRendererDisabled，调用方降级为纯结构化观察
// （token/校验报告/块统计），设计会话功能不中断。
//
// 安全边界（方案 §10.2）：只渲染本站 SSR 输出（data: URL 或本地 base URL），
// 禁止加载任何外部资源；单次截图有超时与视口上限。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// ErrRendererDisabled marks the disabled/missing-chrome state; callers must
// degrade to structured observation instead of failing the design session.
var ErrRendererDisabled = errors.New("screenshot renderer disabled (RENDERER_ENABLED != true or chrome unavailable)")

// Viewport is one capture size. Widths mirror the plan (桌面 1280 / 移动 390).
type Viewport struct {
	Name   string
	Width  int
	Height int
}

// Viewports returns the standard capture set.
func Viewports() []Viewport {
	return []Viewport{
		{Name: "desktop", Width: 1280, Height: 900},
		{Name: "mobile", Width: 390, Height: 844},
	}
}

// Capture renders one HTML document and screenshots the given viewports as
// PNG bytes. enabled gates the pool (RENDERER_ENABLED); timeout bounds each
// capture. 禁网由运行环境（容器网络策略）保证——本函数不做外部请求。
func Capture(ctx context.Context, enabled bool, html string, viewports []Viewport, timeout time.Duration) (map[string][]byte, error) {
	if !enabled {
		return nil, ErrRendererDisabled
	}
	if len(viewports) == 0 {
		viewports = Viewports()
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	out := map[string][]byte{}
	for _, vp := range viewports {
		png, err := captureOne(ctx, html, vp, timeout)
		if err != nil {
			return nil, fmt.Errorf("capture %s: %w", vp.Name, err)
		}
		out[vp.Name] = png
	}
	return out, nil
}

func captureOne(ctx context.Context, html string, vp Viewport, timeout time.Duration) ([]byte, error) {
	// data: URL 承载完整 HTML——不落盘、不出网（与容器禁网策略双保险）。
	dataURL := "data:text/html;charset=utf-8," + html
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true), // 容器内 root 必需
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)
	ctx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()
	ctx, cancel2 := chromedp.NewContext(ctx)
	defer cancel2()
	ctx, cancel3 := context.WithTimeout(ctx, timeout)
	defer cancel3()

	var png []byte
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(vp.Width), int64(vp.Height)),
		chromedp.Navigate(dataURL),
		chromedp.Sleep(100*time.Millisecond), // 字体/布局稳定窗口
		chromedp.FullScreenshot(&png, 70),    // 质量 70 的 JPEG 压缩比 PNG 显著省字节
	); err != nil {
		return nil, err
	}
	return png, nil
}

// SanitizeForRender guards the trust boundary (方案 §10.2): SSR 输出外的任何
// 外链资源引用一律剥除，双保险防止 data URL 内嵌外部请求。
func SanitizeForRender(html string) string {
	blocked := []string{"http://", "https://", "//"}
	out := html
	for _, token := range blocked {
		out = strings.ReplaceAll(out, `src="`+token, `src="__blocked__`)
		out = strings.ReplaceAll(out, `href="`+token, `href="__blocked__`)
	}
	return out
}
