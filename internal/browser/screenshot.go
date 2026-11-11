package browser

import (
	"context"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// ScreenshotJPEG 只截取当前页面的可视区域，不打开新页面。
func (s *Session) ScreenshotJPEG(caller context.Context) ([]byte, error) {
	ctx, cancel := s.readContext(caller)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, operationError(ctx, err)
	}
	quality := int64(65)
	frame, err := chromedp.Call(ctx, page.CaptureScreenshot, page.CaptureScreenshotParams{
		Format: page.CaptureScreenshotFormatJpeg, Quality: &quality,
	})
	return frame.Data, operationError(ctx, err)
}
