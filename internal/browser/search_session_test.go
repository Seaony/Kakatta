package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/chromedp"
)

func TestGetDocumentInvalidatesDOMSearchSession(t *testing.T) {
	requireBrowser(t)
	srv, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{AccountID: 582, ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Navigate(srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(s.Context(), dom.GetDocument, dom.GetDocumentParams{}); err != nil {
		t.Fatal(err)
	}
	found, err := chromedp.Call(s.Context(), dom.PerformSearch, dom.PerformSearchParams{Query: "搜索"})
	if err != nil || found.ResultCount < 1 {
		t.Fatalf("搜索必须成功：%+v %v", found, err)
	}
	if _, err := chromedp.Call(s.Context(), dom.GetDocument, dom.GetDocumentParams{}); err != nil {
		t.Fatal(err)
	}
	_, err = chromedp.Call(s.Context(), dom.GetSearchResults, dom.GetSearchResultsParams{SearchID: found.SearchID, ToIndex: found.ResultCount})
	if err == nil || !strings.Contains(err.Error(), "No search session with given id found") {
		t.Fatalf("重取文档后搜索会话的结果：%v", err)
	}
	t.Log("已确定性复现：搜索已成功，GetDocument清掉搜索会话，GetSearchResults返回同一错误")
}

// 模拟依赖在两条查询命令之间处理文档更新，重放真实的协议时序。
type resettingDOMSession struct {
	cdp.Session
	remaining int
	resets    int
}

func (s *resettingDOMSession) Call(ctx context.Context, method string, params, result any) error {
	if method == dom.CommandGetSearchResults && s.remaining != 0 {
		if s.remaining > 0 {
			s.remaining--
		}
		s.resets++
		if _, err := cdp.Call(ctx, s.Session, dom.GetDocument, dom.GetDocumentParams{}); err != nil {
			return err
		}
	}
	return s.Session.Call(ctx, method, params, result)
}

func TestDOMQueriesRecoverAfterSearchSessionReset(t *testing.T) {
	requireBrowser(t)
	srv, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{AccountID: 583, ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Navigate(srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"文字", "XPath"} {
		t.Run(mode, func(t *testing.T) {
			h := NewHuman(s, pacer.New(nil), pacer.Between(0, 0))
			reset := &resettingDOMSession{Session: h.queries, remaining: 1}
			h.queries = reset
			var nodes []*chromedp.Node
			var err error
			if mode == "文字" {
				nodes, err = h.SearchText(ctx, "搜索")
			} else {
				nodes, err = h.FindNow(ctx, "//button[normalize-space(.)='搜索']")
			}
			if err != nil {
				t.Fatalf("文档重取一次后仍查询失败：%v", err)
			}
			if reset.resets != 1 || len(nodes) != 1 || nodes[0].AttributeValue("id") != "go" {
				t.Fatalf("查询没有恢复正确结果：重取%d次，节点%d个", reset.resets, len(nodes))
			}
		})
	}
	t.Run("持续失效必须报错", func(t *testing.T) {
		h := NewHuman(s, pacer.New(nil), pacer.Between(0, 0))
		reset := &resettingDOMSession{Session: h.queries, remaining: -1}
		h.queries = reset
		_, err := h.SearchText(ctx, "搜索")
		if err == nil || !strings.Contains(err.Error(), "No search session with given id found") || reset.resets != 3 {
			t.Fatalf("持续失效不能当作无结果，也不能无限重试：%d次，%v", reset.resets, err)
		}
	})
}
