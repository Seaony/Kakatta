package browser

import (
	"context"
	"errors"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// ContentScreenOrigin 返回网页内容区左上角在虚拟屏幕上的坐标。
func (s *Session) ContentScreenOrigin(caller context.Context) (left, top int64, err error) {
	ctx, cancel := s.readContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	metrics, err := chromedp.Call(ctx, page.GetLayoutMetrics, cdp.Empty{})
	if err != nil {
		return 0, 0, err
	}
	target := chromedp.FromContext(ctx).Target
	window, err := chromedp.CallBrowser(ctx, cdpbrowser.GetWindowForTarget, cdpbrowser.GetWindowForTargetParams{TargetID: target.TargetID})
	if err != nil {
		return 0, 0, err
	}
	if metrics.CSSLayoutViewport == nil || window.Bounds == nil {
		return 0, 0, errors.New("没有取得浏览器画面大小")
	}
	if window.Bounds.Left != nil {
		left = *window.Bounds.Left
	}
	if window.Bounds.Top != nil {
		top = *window.Bounds.Top
	}
	return left, top + window.Bounds.Height - metrics.CSSLayoutViewport.ClientHeight, nil
}
