package main

import (
	"context"
	"math"
	"time"

	"github.com/Seaony/Kakatta/internal/remote"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func (c *controller) focusVerification(r *resources) {
	ctx, stop := context.WithTimeout(r.chrome.Context(), 5*time.Second)
	defer stop()
	box, err := r.runner.CurrentVerificationBox(ctx)
	if err != nil {
		c.log("未能读取验证框位置", map[string]any{"原因": err.Error()})
		return
	}
	if box == nil {
		return
	}
	metrics, err := chromedp.Call(ctx, page.GetLayoutMetrics, cdp.Empty{})
	if err != nil || metrics.CSSLayoutViewport == nil {
		return
	}
	target := chromedp.FromContext(r.chrome.Context()).Target
	window, err := chromedp.CallBrowser(ctx, cdpbrowser.GetWindowForTarget, cdpbrowser.GetWindowForTargetParams{TargetID: target.TargetID})
	if err != nil || window.Bounds == nil {
		return
	}
	left, top := int64(0), int64(0)
	if window.Bounds.Left != nil {
		left = *window.Bounds.Left
	}
	if window.Bounds.Top != nil {
		top = *window.Bounds.Top
	}
	x := max(0, int(math.Floor(box.X))+int(left)-24)
	y := max(0, int(math.Floor(box.Y))+int(top+window.Bounds.Height-metrics.CSSLayoutViewport.ClientHeight)-24)
	if x >= 1920 || y >= 1080 {
		return
	}
	view := &remote.Viewport{X: x, Y: y, Width: min(1920-x, int(math.Ceil(box.Width))+48), Height: min(1080-y, int(math.Ceil(box.Height))+48)}
	if err := r.remote.SetViewport(view); err != nil {
		c.log("验证框位置不在画面内", nil)
		return
	}
	c.mu.Lock()
	if c.res == r {
		c.state.Viewport = view
	}
	c.mu.Unlock()
}
