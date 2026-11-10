package browser

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Response 是页面自己发出的请求及结果，返回给调用方的都是独立副本。
type Response struct {
	Seq          uint64
	Method       string
	URL          string
	Status       int64
	Headers      map[string]string
	Body         []byte
	Err          error
	MainDocument bool
	done         bool
	updatedAt    time.Time
}

type Match struct {
	URL    string
	Method string
}

func (m Match) matches(r *Response) bool {
	return strings.Contains(r.URL, m.URL) && (m.Method == "" || strings.EqualFold(r.Method, m.Method))
}

var (
	ErrResponseTimeout = errors.New("等待接口响应超时")
	ErrCaptureOverflow = errors.New("在途请求太多，不能可靠判断操作对应的响应")
)

type requestStart struct {
	seq  uint64
	main bool
}

const maxKept = 200

// Capture 只记录需要的接口和主文档，不注入脚本，不保存到磁盘。
type Capture struct {
	ctx       context.Context
	mu        sync.Mutex
	watch     []string
	byID      map[network.RequestID]*Response
	all       []*Response
	seq       uint64
	mainFrame cdp.FrameID
	requests  map[network.RequestID]requestStart
	err       error
	notify    chan struct{}
}

func newCapture(ctx context.Context) *Capture {
	return &Capture{ctx: ctx, byID: map[network.RequestID]*Response{}, requests: map[network.RequestID]requestStart{}, notify: make(chan struct{})}
}

func (c *Capture) Watch(fragments ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watch = append(c.watch, fragments...)
}

func (c *Capture) Mark() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

// Changes 在下一次请求、响应头或正文到达时唤醒等待者。
func (c *Capture) Changes() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.notify
}

func (c *Capture) setMainFrame(id cdp.FrameID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mainFrame = id
}

func (c *Capture) listen() error {
	// 读正文另起协程，不能堵住协议事件回调。
	return chromedp.Listen(c.ctx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventFrameNavigated:
			if e.Frame != nil && e.Frame.ParentID == "" {
				c.setMainFrame(e.Frame.ID)
			}
		case *network.EventRequestWillBeSent:
			if e.Request != nil {
				c.sentRequest(e.RequestID, e.Request, e.Type, e.FrameID, e.RedirectResponse != nil)
			}
		case *network.EventResponseReceived:
			if e.Response != nil {
				c.received(e.RequestID, e.Response)
			}
		case *network.EventLoadingFinished:
			c.forgetStart(e.RequestID)
			if r := c.unfinished(e.RequestID); r != nil {
				go c.fetchBody(e.RequestID, r)
			}
		case *network.EventLoadingFailed:
			c.forgetStart(e.RequestID)
			if r := c.unfinished(e.RequestID); r != nil {
				c.finish(e.RequestID, r, nil, errors.New(e.ErrorText))
			}
		}
	})
}

func (c *Capture) sent(id network.RequestID, req *network.Request) {
	c.sentRequest(id, req, "", "", false)
}

func (c *Capture) sentRequest(id network.RequestID, req *network.Request, typ network.ResourceType, frame cdp.FrameID, redirected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	start, known := c.requests[id]
	if !known {
		if redirected || len(c.requests) >= maxKept {
			c.err = ErrCaptureOverflow
			c.changed()
			return
		}
		c.seq++
		start = requestStart{seq: c.seq, main: typ == network.ResourceTypeDocument && frame == c.mainFrame}
		c.requests[id] = start
	}
	// 跳转仍是同一次请求，不能把操作前发出的请求变成新请求。
	if r := c.byID[id]; r != nil {
		r.URL, r.Method, r.updatedAt = req.URL, req.Method, time.Now()
		c.changed()
		return
	}
	main := start.main
	watched := main
	for _, f := range c.watch {
		watched = watched || strings.Contains(req.URL, f)
	}
	if !watched {
		return
	}
	r := &Response{Seq: start.seq, Method: req.Method, URL: req.URL, MainDocument: main, updatedAt: time.Now()}
	c.byID[id] = r
	c.all = append(c.all, r)
	slices.SortFunc(c.all, func(a, b *Response) int {
		if a.Seq < b.Seq {
			return -1
		}
		if a.Seq > b.Seq {
			return 1
		}
		return 0
	})
	if len(c.all) > maxKept {
		old := c.all[0]
		for id, pending := range c.byID {
			if pending == old {
				c.err = ErrCaptureOverflow
				delete(c.byID, id)
				break
			}
		}
		copy(c.all, c.all[1:])
		c.all = c.all[:maxKept]
	}
	c.changed()
}

func (c *Capture) received(id network.RequestID, resp *network.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.byID[id]; r != nil {
		r.URL, r.Status, r.Headers = resp.URL, resp.Status, lowerHeaders(resp.Headers)
		if r.MainDocument {
			// 主文档只检查响应头，收到处罚头时不必等正文。
			r.done = true
			delete(c.byID, id)
		}
		c.changed()
	}
}

func (c *Capture) unfinished(id network.RequestID) *Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.byID[id]; r != nil && !r.done {
		return r
	}
	return nil
}

func (c *Capture) fetchBody(id network.RequestID, r *Response) {
	res, err := chromedp.Call(c.ctx, network.GetResponseBody, network.GetResponseBodyParams{RequestID: id})
	// 当前协议库会在解码时处理 base64Encoded。
	c.finish(id, r, res.Body, err)
}

func (c *Capture) finish(id network.RequestID, r *Response, body []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID[id] != r {
		return
	}
	r.Body, r.Err, r.done, r.updatedAt = slices.Clone(body), err, true, time.Now()
	delete(c.byID, id)
	delete(c.requests, id)
	c.changed()
}

func (c *Capture) forgetStart(id network.RequestID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.requests, id)
}

func (c *Capture) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Capture) changed() {
	close(c.notify)
	c.notify = make(chan struct{})
}

func responseCopy(r *Response) *Response {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Headers, copy.Body = maps.Clone(r.Headers), slices.Clone(r.Body)
	return &copy
}

func (c *Capture) SentAfter(mark uint64, m Match) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.all {
		if r.Seq > mark && m.matches(r) {
			return true
		}
	}
	return false
}

func (c *Capture) WaitAfter(ctx context.Context, mark uint64, m Match, timeout time.Duration) (*Response, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		if c.err != nil {
			err := c.err
			c.mu.Unlock()
			return nil, err
		}
		for _, r := range c.all {
			if r.Seq > mark && r.done && m.matches(r) {
				out := responseCopy(r)
				c.mu.Unlock()
				return out, nil
			}
		}
		wake := c.notify
		c.mu.Unlock()
		select {
		case <-wake:
		case <-deadline.C:
			return nil, ErrResponseTimeout
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		}
	}
}

// WaitLastAfter 等在途请求都结束，再等 quiet 没有新请求，返回最后发出的那条。
func (c *Capture) WaitLastAfter(ctx context.Context, mark uint64, m Match, quiet, timeout time.Duration) (*Response, error) {
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		if c.err != nil {
			err := c.err
			c.mu.Unlock()
			return nil, err
		}
		var last *Response
		var activity time.Time
		pending := false
		for _, r := range c.all {
			if r.Seq <= mark || !m.matches(r) {
				continue
			}
			last = r
			pending = pending || !r.done
			if r.updatedAt.After(activity) {
				activity = r.updatedAt
			}
		}
		if last != nil && !pending && time.Since(activity) >= quiet {
			out := responseCopy(last)
			c.mu.Unlock()
			return out, nil
		}
		wake := c.notify
		wait := time.Until(deadline)
		if last != nil && !pending {
			wait = min(wait, time.Until(activity.Add(quiet)))
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			return nil, ErrResponseTimeout
		}
		timer := time.NewTimer(max(wait, time.Nanosecond))
		select {
		case <-wake:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-c.ctx.Done():
			timer.Stop()
			return nil, c.ctx.Err()
		}
		timer.Stop()
	}
}

func (c *Capture) LastAfter(mark uint64, m Match) *Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.all) - 1; i >= 0; i-- {
		r := c.all[i]
		if r.Seq > mark && r.done && m.matches(r) {
			return responseCopy(r)
		}
	}
	return nil
}

// HeadersAfter 包括正文还没收完的响应，处罚头一到就能停止操作。
func (c *Capture) HeadersAfter(mark uint64, m Match) []*Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Response
	for _, r := range c.all {
		if r.Seq > mark && r.Headers != nil && m.matches(r) {
			out = append(out, responseCopy(r))
		}
	}
	return out
}

func (c *Capture) DoneAfter(mark uint64, m Match) []*Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Response
	for _, r := range c.all {
		if r.Seq > mark && r.done && m.matches(r) {
			out = append(out, responseCopy(r))
		}
	}
	return out
}

func lowerHeaders(h network.Headers) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if s, ok := v.(string); ok {
			out[strings.ToLower(k)] = s
		}
	}
	return out
}
