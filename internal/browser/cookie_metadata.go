package browser

import (
	"context"

	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

type CookieMetadata struct {
	Name    string  `json:"name"`
	Domain  string  `json:"domain"`
	Session bool    `json:"session"`
	Expires float64 `json:"expires"`
}

// CookieMetadata 只取整个浏览器配置的 Cookie 元数据，不查看或返回值。
func (s *Session) CookieMetadata(caller context.Context) ([]CookieMetadata, error) {
	ctx, cancel := s.readContext(caller)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, operationError(ctx, err)
	}
	result, err := chromedp.CallBrowser(ctx, storage.GetCookies, storage.GetCookiesParams{})
	if err != nil {
		return nil, operationError(ctx, err)
	}
	metadata := make([]CookieMetadata, 0, len(result.Cookies))
	for _, cookie := range result.Cookies {
		if cookie != nil {
			metadata = append(metadata, CookieMetadata{Name: cookie.Name, Domain: cookie.Domain, Session: cookie.Session, Expires: cookie.Expires})
		}
	}
	return metadata, nil
}
