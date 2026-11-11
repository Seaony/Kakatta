package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"golang.org/x/net/html"
)

type DiagnosticPage struct {
	Screenshot []byte
	HTML       string
	URL        string
	Title      string
	Errors     map[string]string
	LoginPage  bool
}

// DiagnosticPage 只读取当前主文档，与页面查找共用读取许可，不执行脚本。
func (s *Session) DiagnosticPage(caller context.Context) (result DiagnosticPage, err error) {
	ctx, cancel := s.readContext(caller)
	defer cancel()
	result.Errors = map[string]string{}
	result.LoginPage = true
	if err := ctx.Err(); err != nil {
		return result, operationError(ctx, err)
	}
	lookup := &Human{s: s, domRead: s.domReadPermit()}
	unlock, err := lookup.lockDOM(ctx)
	if err != nil {
		return result, operationError(ctx, err)
	}
	defer unlock()
	failed := func(part string, failure error) {
		if failure != nil {
			result.Errors[part] = failure.Error()
			err = errors.Join(err, failure)
		}
	}
	quality := int64(65)
	shot, failure := chromedp.Call(ctx, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatJpeg, Quality: &quality})
	result.Screenshot = shot.Data
	failed("screenshot", failure)
	tree, failure := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{})
	if failure == nil && (tree.FrameTree == nil || tree.FrameTree.Frame == nil) {
		failure = errors.New("读不到主框架网址")
	}
	if failure == nil {
		result.URL = tree.FrameTree.Frame.URL + tree.FrameTree.Frame.URLFragment
	}
	failed("url", failure)
	depth := int64(0)
	root, failure := chromedp.Call(ctx, dom.GetDocument, dom.GetDocumentParams{Depth: &depth})
	if failure == nil && root.Root == nil {
		failure = errors.New("读不到主文档")
	}
	if failure == nil {
		var document dom.GetOuterHTMLResult
		document, failure = chromedp.Call(ctx, dom.GetOuterHTML, dom.GetOuterHTMLParams{BackendNodeID: root.Root.BackendNodeID})
		result.HTML = document.OuterHTML
		result.Title = documentTitle(result.HTML)
	}
	failed("html", failure)
	if result.Errors["url"] == "" && result.Errors["html"] == "" {
		result.LoginPage = loginDocument(result.URL, result.HTML)
	}
	return result, operationError(ctx, err)
}

func loginDocument(url, document string) bool {
	lowerURL := strings.ToLower(url)
	for _, marker := range []string{"login", "passport", "oauth", "qrcode"} {
		if strings.Contains(lowerURL, marker) {
			return true
		}
	}
	lowerHTML := strings.ToLower(document)
	for _, marker := range []string{"alibaba-login-box", "fm-login-id", "login-iframe", "qrlogin", "qrcode"} {
		if strings.Contains(lowerHTML, marker) {
			return true
		}
	}
	return false
}

func (s *Session) Version(caller context.Context) (string, error) {
	ctx, cancel := s.readContext(caller)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", operationError(ctx, err)
	}
	version, err := chromedp.CallBrowser(ctx, cdpbrowser.GetVersion, cdp.Empty{})
	if err == nil && version.Product == "" {
		err = fmt.Errorf("读不到实际 Chrome 版本")
	}
	return version.Product, operationError(ctx, err)
}

func documentTitle(document string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(document))
	for tokenizer.Next() != html.ErrorToken {
		token := tokenizer.Token()
		if token.Data == "title" && token.Type == html.StartTagToken {
			if tokenizer.Next() == html.TextToken {
				return string(tokenizer.Text())
			}
		}
	}
	return ""
}
