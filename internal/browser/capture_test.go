package browser

import (
	"context"
	"fmt"
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
