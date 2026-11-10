package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func captureForTest() *Capture {
	c := newCapture(context.Background())
	c.Watch("/api/")
	return c
}

func finishForTest(c *Capture, id network.RequestID, body string) {
	r := c.unfinished(id)
	c.received(id, &network.Response{URL: "http://local/api/" + string(id), Status: 200, Headers: network.Headers{"X-Test": "value"}})
	c.finish(id, r, []byte(body), nil)
}

func TestCaptureReturnsIndependentSnapshots(t *testing.T) {
	c := captureForTest()
	c.sent("1", &network.Request{URL: "http://local/api/1", Method: "POST"})
	finishForTest(c, "1", "body")
	first, err := c.WaitAfter(context.Background(), 0, Match{URL: "/api/"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first.Body[0] = 'X'
	first.Headers["x-test"] = "changed"
	second := c.LastAfter(0, Match{})
	if string(second.Body) != "body" || second.Headers["x-test"] != "value" {
		t.Fatal("调用方改动影响了捕获器里的记录")
	}
}

func TestCaptureUsesOriginalSequenceAcrossUnwatchedRedirect(t *testing.T) {
	c := captureForTest()
	c.sent("old", &network.Request{URL: "http://local/before", Method: "GET"})
	mark := c.Mark()
	c.sent("new", &network.Request{URL: "http://local/api/new", Method: "POST"})
	c.sentRequest("old", &network.Request{URL: "http://local/api/old", Method: "GET"}, "", "", true)
	finishForTest(c, "old", "old")
	finishForTest(c, "new", "new")
	resp, err := c.WaitAfter(context.Background(), mark, Match{}, time.Second)
	if err != nil || string(resp.Body) != "new" {
		t.Fatalf("旧请求的跳转被当成了本次响应：%v %v", resp, err)
	}
	resp, err = c.WaitAfter(context.Background(), 0, Match{}, time.Second)
	if err != nil || string(resp.Body) != "old" {
		t.Fatalf("第一条响应没有按初始请求顺序选择：%v %v", resp, err)
	}
	if resp.RequestURL != "http://local/before" || resp.RequestMethod != "GET" {
		t.Fatal("未关注的原始请求也必须保留来源", resp)
	}
	resp, err = c.WaitLastAfter(context.Background(), 0, Match{}, time.Millisecond, time.Second)
	if err != nil || string(resp.Body) != "new" {
		t.Fatalf("最后一条响应没有按初始请求顺序选择：%v %v", resp, err)
	}
}

func TestCaptureWaitsForInflightAndQuietPeriod(t *testing.T) {
	c := captureForTest()
	c.sent("1", &network.Request{URL: "http://local/api/1", Method: "POST"})
	finishForTest(c, "1", "first")
	quiet := 40 * time.Millisecond
	result := make(chan *Response, 1)
	go func() {
		r, _ := c.WaitLastAfter(context.Background(), 0, Match{}, quiet, time.Second)
		result <- r
	}()
	time.Sleep(10 * time.Millisecond)
	c.sent("2", &network.Request{URL: "http://local/api/2", Method: "POST"})
	time.Sleep(2 * quiet)
	select {
	case <-result:
		t.Fatal("还有请求在路上时提前用了旧结果")
	default:
	}
	start := time.Now()
	finishForTest(c, "2", "last")
	r := <-result
	if r == nil || string(r.Body) != "last" || time.Since(start) < quiet {
		t.Fatal("没有等最后一次响应之后的静默期")
	}
}

func TestCaptureHeadersAvailableBeforeBody(t *testing.T) {
	c := captureForTest()
	c.sent("1", &network.Request{URL: "http://local/api/1", Method: "POST"})
	c.received("1", &network.Response{URL: "http://local/api/final", Headers: network.Headers{"BxPunish": "1"}})
	head := c.HeadersAfter(0, Match{})
	if len(head) != 1 || head[0].Headers["bxpunish"] != "1" || head[0].URL != "http://local/api/final" {
		t.Fatal("正文没结束前读不到风控响应头或最终网址")
	}
	if len(c.DoneAfter(0, Match{})) != 0 {
		t.Fatal("正文没结束却被当成收完了")
	}
}

func TestCaptureMainDocumentHeaderNeedsNoBody(t *testing.T) {
	c := captureForTest()
	c.setMainFrame("main")
	c.sentRequest("1", &network.Request{URL: "http://local/home", Method: "GET"}, network.ResourceTypeDocument, "main", false)
	c.received("1", &network.Response{URL: "http://local/home", Headers: network.Headers{"bxpunish": "1"}})
	r := c.DoneAfter(0, Match{})
	if len(r) != 1 || !r[0].MainDocument || r[0].Headers["bxpunish"] != "1" {
		t.Fatal("主文档处罚头没记录")
	}
}

func TestCaptureBoundsPendingAndDoesNotLoseSlowRiskResponse(t *testing.T) {
	c := captureForTest()
	c.sent("slow", &network.Request{URL: "http://local/api/slow", Method: "POST"})
	for i := 0; i < maxKept; i++ {
		id := network.RequestID(fmt.Sprint(i))
		c.sent(id, &network.Request{URL: "http://local/api/" + string(id), Method: "POST"})
		finishForTest(c, id, "ok")
	}
	if len(c.all) > maxKept || len(c.byID) > maxKept || len(c.requests) > maxKept {
		t.Fatal("捕获记录超过内存上限")
	}
	if c.Err() != ErrCaptureOverflow {
		t.Fatal("慢响应被淘汰时必须停止，不能丢掉可能的风控信号")
	}
	if _, err := c.WaitAfter(context.Background(), 0, Match{}, time.Second); err != ErrCaptureOverflow {
		t.Fatal("捕获不完整时不应继续取结果")
	}
}

func TestCaptureBoundsUnwatchedInflightMetadata(t *testing.T) {
	c := captureForTest()
	for i := 0; i <= maxKept; i++ {
		c.sent(network.RequestID(fmt.Sprint(i)), &network.Request{URL: "http://local/other", Method: "GET"})
	}
	if c.Err() != ErrCaptureOverflow || len(c.requests) > maxKept {
		t.Fatal("请求元数据无界增长或丢失时继续运行")
	}
}

func TestCaptureArrivalOrderIncludesLatestHeaderAndFailure(t *testing.T) {
	c := captureForTest()
	c.sent("first", &network.Request{URL: "http://local/api/first", Method: "POST"})
	c.received("first", &network.Response{URL: "http://local/api/redirect", Status: 302})
	initial := c.HeadersAfter(0, Match{})[0]
	c.sentRequest("first", &network.Request{URL: "http://local/api/final", Method: "POST"}, "", "", true)
	c.sent("second", &network.Request{URL: "http://local/api/second", Method: "POST"})
	finishForTest(c, "second", "ok")
	c.received("first", &network.Response{URL: "http://local/api/final", Status: 200})
	finalHeader := c.HeadersAfter(0, Match{URL: "/final"})[0]
	if initial.HeaderOrder == 0 || finalHeader.HeaderOrder <= initial.HeaderOrder || finalHeader.Seq != initial.Seq || finalHeader.BodyOrder != 0 {
		t.Fatalf("最终响应头必须更新到达顺序且不改请求编号：%+v %+v", initial, finalHeader)
	}
	c.finish("first", c.unfinished("first"), nil, errors.New("连接中断"))
	failed, err := c.WaitAfter(context.Background(), 0, Match{URL: "/final"}, time.Second)
	if err != nil || failed.Err == nil || failed.BodyOrder <= finalHeader.HeaderOrder {
		t.Fatalf("失败完成没有到达编号：%+v %v", failed, err)
	}
	second := c.LastAfter(0, Match{URL: "/second"})
	if second.HeaderOrder <= initial.HeaderOrder || second.BodyOrder <= second.HeaderOrder || failed.BodyOrder <= second.BodyOrder {
		t.Fatalf("各阶段到达顺序错误：先请求%+v，后请求%+v", failed, second)
	}
}

func TestCaptureBodyOrderFollowsLocalResponseCompletion(t *testing.T) {
	requireBrowser(t)
	releaseSlow := make(chan struct{})
	var release sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/slow":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-releaseSlow:
				fmt.Fprint(w, `{"ret":["RGV587"],"data":{}}`)
			case <-r.Context().Done():
			}
		case "/api/fast":
			fmt.Fprint(w, `{"ret":["SUCCESS::OK"],"data":{}}`)
		default:
			w.Header().Set("Content-Type", "text/html;charset=utf-8")
			fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><script>fetch('/api/slow').then(()=>fetch('/api/fast'));</script>`)
		}
	}))
	defer srv.Close()
	defer release.Do(func() { close(releaseSlow) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{AccountID: 584, ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Capture.Watch("/api/")
	mark := s.Capture.Mark()
	if err := s.Navigate(srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	fast, err := s.Capture.WaitAfter(ctx, mark, Match{URL: "/api/fast"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	slowHeader := s.Capture.HeadersAfter(mark, Match{URL: "/api/slow"})
	if len(slowHeader) != 1 || slowHeader[0].HeaderOrder == 0 || slowHeader[0].BodyOrder != 0 {
		t.Fatalf("慢请求应已收到响应头但正文仍在途：%+v", slowHeader)
	}
	release.Do(func() { close(releaseSlow) })
	slow, err := s.Capture.WaitAfter(ctx, mark, Match{URL: "/api/slow"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if slow.Seq >= fast.Seq || slow.HeaderOrder >= fast.HeaderOrder || fast.BodyOrder >= slow.BodyOrder || slow.BodyOrder == 0 || fast.BodyOrder <= fast.HeaderOrder {
		t.Fatalf("请求先后不能代替到达顺序：先发慢请求%+v，后发快请求%+v", slow, fast)
	}
	if !strings.Contains(string(slow.Body), "RGV587") || !strings.Contains(string(fast.Body), "SUCCESS") {
		t.Fatal("样例响应没有分别保留风控和成功信号")
	}
}

func TestCaptureKeepsAPIMatchesAfterRedirect(t *testing.T) {
	c := newCapture(context.Background())
	c.Watch("/h5/mtop.")
	c.sent("redirect", &network.Request{URL: "http://local/h5/mtop.fake/1.0/", Method: "POST"})
	beforeRedirect := c.Mark()
	c.sentRequest("redirect", &network.Request{URL: "http://local/punish?action=deny", Method: "GET"}, "", "", true)
	c.received("redirect", &network.Response{URL: "http://local/punish?action=deny", Status: 200, Headers: network.Headers{"bxpunish": "1"}})
	c.finish("redirect", c.unfinished("redirect"), []byte(`{"ret":["SUCCESS::OK"],"data":{"url":"http://local/punish?action=deny"}}`), nil)
	api := Match{URL: "/h5/mtop.fake/", Method: "POST"}
	if headers, body := c.HeadersAfter(0, api), c.DoneAfter(0, api); len(headers) != 1 || len(body) != 1 {
		t.Fatalf("重定向后不能丢掉原API的响应头和正文：头%d，正文%d", len(headers), len(body))
	}
	r := c.LastAfter(0, api)
	if r.URL != "http://local/punish?action=deny" || r.Method != "GET" || r.MainDocument || r.Seq != beforeRedirect || c.Mark() != beforeRedirect {
		t.Fatalf("重定向不能改请求编号、丢最终信息或变成主文档：%+v", r)
	}
	if c.SentAfter(beforeRedirect, api) {
		t.Fatal("重定向不能当作操作之后发出的新请求")
	}
	if (Match{URL: "/h5/mtop.fake/", Method: "GET"}).matches(r) || (Match{URL: "/punish", Method: "POST"}).matches(r) {
		t.Fatal("地址和方法不能从原始与最终两边混用")
	}
}

func TestCaptureLocalAPIRedirectRetainsOriginalRequest(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/h5/mtop.fake/1.0/":
			if r.Method != "POST" {
				t.Errorf("原始请求方法错误：%s", r.Method)
			}
			http.Redirect(w, r, "/punish?action=deny", http.StatusFound)
		case "/punish":
			if r.Method != "GET" {
				t.Errorf("302后请求方法错误：%s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("bxpunish", "1")
			fmt.Fprint(w, `{"ret":["SUCCESS::OK"],"data":{"url":"http://local/punish?action=deny"}}`)
		default:
			w.Header().Set("Content-Type", "text/html;charset=utf-8")
			fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><script>fetch('/h5/mtop.fake/1.0/',{method:'POST'});</script>`)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{AccountID: 585, ProfileDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Capture.Watch("/h5/mtop.")
	mark := s.Capture.Mark()
	if err := s.Navigate(srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	api := Match{URL: "/h5/mtop.fake/1.0/", Method: "POST"}
	r, err := s.Capture.WaitAfter(ctx, mark, api, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if r.RequestURL != srv.URL+"/h5/mtop.fake/1.0/" || r.RequestMethod != "POST" || r.URL != srv.URL+"/punish?action=deny" || r.Method != "GET" || r.MainDocument {
		t.Fatalf("真实重定向的原始/最终请求或页面类型错误：%+v", r)
	}
	headers := s.Capture.HeadersAfter(mark, api)
	if len(headers) != 1 || headers[0].Headers["bxpunish"] != "1" || !strings.Contains(string(r.Body), "action=deny") {
		t.Fatal("真实重定向后的风控头或正文被漏掉")
	}
	pageURL, err := s.URL()
	if err != nil || strings.TrimSuffix(pageURL, "/") != srv.URL {
		t.Fatalf("XHR最终验证网址不能代替主框架网址：%s %v", pageURL, err)
	}
}
