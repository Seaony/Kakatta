//go:build kakatta_dev

package remote

import (
	"context"
	"errors"
)

// StartDevelopmentNativePreview 只提供本机假页的截图，操作留在独立浏览器窗口。
func StartDevelopmentNativePreview(ctx context.Context, capture func(context.Context) ([]byte, error)) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if capture == nil {
		return nil, errors.New("本机假页画面缺少截图来源")
	}
	lifetime, cancel := context.WithCancel(ctx)
	return &Session{ctx: lifetime, cancel: cancel, preview: &readOnlyPreview{
		noticeCode: "development_native_notice", capture: capture,
	}}, nil
}
