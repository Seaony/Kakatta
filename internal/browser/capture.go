package browser

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Response 是页面自己发出的一次接口请求，以及它的结果。
type Response struct {
	Seq     uint64 // 请求发出时的编号，越大越晚发出
	Method  string
	URL     string
	Status  int64
	Headers map[string]string // 响应头，键统一小写
	Body    []byte
	Err     error // 请求失败或取不到正文时的原因
	done    bool  // 正文已经取到，或者请求已经失败
}

// Match 描述要等的是哪一种请求。Method 为空时不限请求方法。
type Match struct {
	URL    string // 网址里要含的片段
	Method string // 比如 "POST"
}

func (m Match) matches(r *Response) bool {
	return strings.Contains(r.URL, m.URL) && (m.Method == "" || strings.EqualFold(r.Method, m.Method))
}

// ErrResponseTimeout 表示在规定时间内没有等到想要的接口响应。
var ErrResponseTimeout = errors.New("等待接口响应超时")

// maxKept 是最多保留多少条请求记录，够一次运行用，又不会一直占内存。
const maxKept = 200

// Capture 通过 CDP 网络事件读取页面自己请求到的数据，不往页面里注入脚本。
// 编号在请求发出时分配：操作之前就发出、操作之后才返回的旧请求，不会被当成这次操作的结果。
type Capture struct {
	ctx    context.Context
	mu     sync.Mutex
	watch  []string // 只关心网址里含这些片段的请求
	byID   map[network.RequestID]*Response
	all    []*Response // 按发出顺序排列
	seq    uint64
	notify chan struct{} // 有请求完成时关闭并换一个新的，用来唤醒等待者
}

func newCapture(ctx context.Context) *Capture {
	return &Capture{ctx: ctx, byID: map[network.RequestID]*Response{}, notify: make(chan struct{})}
}

// Watch 设置要关心的接口网址片段，比如 "mtop.taobao.idlemtopsearch.pc.search"。
func (c *Capture) Watch(fragments ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watch = append(c.watch, fragments...)
}

// Mark 返回当前的编号。之后只认这个编号之后才发出的请求。
func (c *Capture) Mark() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

func (c *Capture) listen() error {
	// 回调在读取浏览器消息的循环里执行，不能阻塞，也不能在里面发命令；取正文放到单独的 goroutine。
	return chromedp.Listen(c.ctx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			if e.Request != nil {
				c.sent(e.RequestID, e.Request)
			}
		case *network.EventResponseReceived:
			if e.Response != nil {
				c.received(e.RequestID, e.Response)
			}
		case *network.EventLoadingFinished:
			if r := c.unfinished(e.RequestID); r != nil {
				go c.fetchBody(e.RequestID, r)
			}
		case *network.EventLoadingFailed:
			if r := c.unfinished(e.RequestID); r != nil {
				c.finish(e.RequestID, r, nil, errors.New(e.ErrorText))
			}
		}
	})
}

func (c *Capture) sent(id network.RequestID, req *network.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	watched := false
	for _, f := range c.watch {
		if strings.Contains(req.URL, f) {
			watched = true
			break
		}
	}
	if !watched {
		return
	}
	c.seq++
	r := &Response{Seq: c.seq, Method: req.Method, URL: req.URL}
	c.byID[id] = r
	c.all = append(c.all, r)
	if len(c.all) > maxKept {
		c.all = c.all[len(c.all)-maxKept:]
	}
}

func (c *Capture) received(id network.RequestID, resp *network.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.byID[id]; r != nil {
		r.Status = resp.Status
		r.Headers = lowerHeaders(resp.Headers)
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

// fetchBody 在响应加载完成之后再取正文；页面跳转后资源会被丢弃，所以要尽快取。
func (c *Capture) fetchBody(id network.RequestID, r *Response) {
	res, err := chromedp.Call(c.ctx, network.GetResponseBody, network.GetResponseBodyParams{RequestID: id})
	c.finish(id, r, res.Body, err)
}

func (c *Capture) finish(id network.RequestID, r *Response, body []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r.Body, r.Err, r.done = body, err, true
	delete(c.byID, id)
	close(c.notify)
	c.notify = make(chan struct{})
}

// SentAfter 判断 mark 之后有没有发出过符合条件的请求（不管有没有返回）。
// 筛选或翻页超时时用它判断：请求已经发出就不要再点一次，否则可能多翻一页，或者把刚开的筛选又取消掉。
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

// WaitAfter 等 mark 之后发出、符合条件、已经完成的第一条请求，最多等 timeout。
func (c *Capture) WaitAfter(ctx context.Context, mark uint64, m Match, timeout time.Duration) (*Response, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		for _, r := range c.all {
			if r.Seq > mark && r.done && m.matches(r) {
				c.mu.Unlock()
				return r, nil
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
		}
	}
}

// LastAfter 返回 mark 之后发出、符合条件、已经完成的最后一条请求；没有就返回 nil。
// 筛选时页面可能连发几次请求，只有最后一次才是筛选完成后的结果。
func (c *Capture) LastAfter(mark uint64, m Match) *Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.all) - 1; i >= 0; i-- {
		r := c.all[i]
		if r.Seq <= mark {
			break
		}
		if r.done && m.matches(r) {
			return r
		}
	}
	return nil
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
