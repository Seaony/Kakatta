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

	"github.com/chromedp/cdproto/cdp"
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
	c.sentRequest("old", &network.Request{URL: "http://local/api/old", Method: "GET"}, "", "", &network.Response{URL: "http://local/before", Status: 302})
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
	c.sentRequest("1", &network.Request{URL: "http://local/home", Method: "GET"}, network.ResourceTypeDocument, "main", nil)
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
	c.sentRequest("first", &network.Request{URL: "http://local/api/final", Method: "POST"}, "", "", &network.Response{URL: "http://local/api/redirect", Status: 302})
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
	c.sentRequest("redirect", &network.Request{URL: "http://local/punish?action=deny", Method: "GET"}, "", "", &network.Response{URL: "http://local/h5/mtop.fake/1.0/", Status: 302})
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

func TestCaptureRedirectPunishmentBeforeFinalResponse(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		kind  network.ResourceType
		frame cdp.FrameID
	}{
		{"主文档", "/home", network.ResourceTypeDocument, "main"},
		{"接口", "/h5/mtop.fake/1.0/", network.ResourceTypeXHR, "main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newCapture(context.Background())
			c.Watch("/h5/mtop.")
			c.setMainFrame("main")
			const landing, onward, final = "http://local/landing", "http://local/onward", "http://local/final"
			first := "http://local" + test.path
			c.sentRequest("redirect", &network.Request{URL: first, Method: "POST"}, test.kind, test.frame, nil)
			seq := c.Mark()
			changed := c.Changes()
			c.sentRequest("redirect", &network.Request{URL: landing, Method: "GET"}, test.kind, test.frame,
				&network.Response{URL: first, Status: 302, Headers: network.Headers{"BxPunish": "1"}})
			select {
			case <-changed:
			default:
				t.Fatal("处罚头没有唤醒等待者")
			}
			original := Match{URL: first, Method: "POST"}
			headers := c.HeadersAfter(0, original)
			if len(headers) != 1 || headers[0].Headers["bxpunish"] != "1" || headers[0].HeaderOrder == 0 {
				t.Fatalf("最终响应前没有读到处罚头：%+v", headers)
			}
			previous := headers[0]
			if previous.URL != first || previous.Method != "POST" || previous.Status != 302 || previous.Seq != seq ||
				previous.MainDocument != (test.kind == network.ResourceTypeDocument) || len(c.DoneAfter(0, original)) != 0 {
				t.Fatalf("处罚快照的请求信息错误或提前完成请求：%+v", previous)
			}
			previous.Headers["bxpunish"] = "改动副本"
			if c.HeadersAfter(0, original)[0].Headers["bxpunish"] != "1" {
				t.Fatal("改动处罚头副本影响了捕获记录")
			}

			c.sent("second", &network.Request{URL: "http://local/h5/mtop.second/1.0/", Method: "GET"})
			c.received("second", &network.Response{URL: "http://local/h5/mtop.second/1.0/", Status: 200})
			c.finish("second", c.unfinished("second"), []byte("其他接口响应"), nil)
			other := c.LastAfter(0, Match{URL: "mtop.second"})
			c.sentRequest("redirect", &network.Request{URL: onward, Method: "GET"}, test.kind, test.frame,
				&network.Response{URL: landing, Status: 307, Headers: network.Headers{"bxpunish": "1"}})
			headers = c.HeadersAfter(0, original)
			if len(headers) != 1 || headers[0].URL != landing || headers[0].Method != "GET" || headers[0].Seq != seq ||
				!(previous.HeaderOrder < other.BodyOrder && other.BodyOrder < headers[0].HeaderOrder) {
				t.Fatalf("同一请求后续处罚必须按到达顺序刷新，先前处罚=%+v，其他响应=%+v，后续头=%+v", previous, other, headers)
			}
			riskOrder := headers[0].HeaderOrder

			c.sentRequest("redirect", &network.Request{URL: final, Method: "GET"}, test.kind, test.frame,
				&network.Response{URL: onward, Status: 302})
			c.received("redirect", &network.Response{URL: final, Status: 200, Headers: network.Headers{"X-Test": "final"}})
			if test.kind != network.ResourceTypeDocument {
				c.finish("redirect", c.unfinished("redirect"), []byte("最终正文"), nil)
			}
			resp, err := c.WaitAfter(context.Background(), 0, original, time.Second)
			if err != nil || resp.Seq != seq || resp.HeaderOrder <= riskOrder || resp.Headers["x-test"] != "final" ||
				resp.Headers["bxpunish"] != "" || resp.URL != final || resp.Method != "GET" ||
				resp.RequestURL != first || resp.RequestMethod != "POST" {
				t.Fatalf("请求等待没有返回独立的最终响应：%+v %v", resp, err)
			}
			if test.kind != network.ResourceTypeDocument && (string(resp.Body) != "最终正文" || resp.BodyOrder <= resp.HeaderOrder) {
				t.Fatalf("最终接口正文或到达顺序错误：%+v", resp)
			}
			headers = c.HeadersAfter(0, original)
			if len(headers) != 2 || headers[0].HeaderOrder != riskOrder || headers[0].Headers["bxpunish"] != "1" ||
				headers[1].HeaderOrder != resp.HeaderOrder || len(c.DoneAfter(0, original)) != 1 {
				t.Fatalf("最终响应覆盖了处罚头或产生了额外的完成响应：%+v", headers)
			}
		})
	}
}

func TestCaptureRedirectPunishmentOnlyOnRelevantHop(t *testing.T) {
	c := newCapture(context.Background())
	c.Watch("/h5/mtop.")
	c.setMainFrame("main")
	const image, child, api, final = "http://local/image", "http://local/child", "http://local/h5/mtop.fake/1.0/", "http://local/final"
	punishment := func(url string) *network.Response {
		return &network.Response{URL: url, Status: 302, Headers: network.Headers{"BxPunish": "1"}}
	}
	c.sentRequest("child", &network.Request{URL: child, Method: "GET"}, network.ResourceTypeDocument, "subframe", nil)
	c.sentRequest("child", &network.Request{URL: final, Method: "GET"}, network.ResourceTypeDocument, "subframe", punishment(child))
	c.sentRequest("image", &network.Request{URL: image, Method: "GET"}, network.ResourceTypeImage, "main", nil)
	c.sentRequest("image", &network.Request{URL: api, Method: "GET"}, network.ResourceTypeImage, "main", punishment(image))
	if got := c.HeadersAfter(0, Match{}); len(got) != 0 {
		t.Fatalf("子框架和跳向接口的普通图片头不应当作风控：%+v", got)
	}
	c.sentRequest("image", &network.Request{URL: final, Method: "GET"}, network.ResourceTypeImage, "main", punishment(api))
	got := c.HeadersAfter(0, Match{URL: api})
	if len(got) != 1 || got[0].MainDocument || got[0].RequestURL != image || got[0].URL != api {
		t.Fatalf("请求已经到了接口，这一跳的处罚应当可见：%+v", got)
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
			w.Header().Set("bxpunish", "1")
			http.Redirect(w, r, "/punish?action=deny", http.StatusFound)
		case "/punish":
			if r.Method != "GET" {
				t.Errorf("302后请求方法错误：%s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
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
	if len(headers) != 2 || headers[0].Status != 302 || headers[0].Headers["bxpunish"] != "1" ||
		headers[0].Seq != r.Seq || headers[0].HeaderOrder >= r.HeaderOrder || r.Headers["bxpunish"] != "" ||
		!strings.Contains(string(r.Body), "action=deny") {
		t.Fatalf("浏览器的重定向处罚头和正常最终正文应同时保留：%+v %+v", headers, r)
	}
	pageURL, err := s.URL()
	if err != nil || strings.TrimSuffix(pageURL, "/") != srv.URL {
		t.Fatalf("XHR最终验证网址不能代替主框架网址：%s %v", pageURL, err)
	}
}
