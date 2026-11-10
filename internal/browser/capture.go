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

// Response 是页面自己发出的一次接口请求的结果。
type Response struct {
	Seq     uint64 // 收到的先后编号，越大越晚
	URL     string
	Status  int64
	Headers map[string]string // 响应头，键统一小写
	Body    []byte
	Err     error // 请求失败或取不到正文时的原因
}

// ErrResponseTimeout 表示在规定时间内没有等到想要的接口响应。
var ErrResponseTimeout = errors.New("等待接口响应超时")

// maxKept 是最多保留多少条已收到的响应，够一次运行用，又不会一直占内存。
const maxKept = 200

// Capture 通过 CDP 网络事件读取页面自己请求到的数据，不往页面里注入脚本。
type Capture struct {
	ctx     context.Context
	mu      sync.Mutex
	watch   []string                        // 只关心网址里含这些片段的请求
	pending map[network.RequestID]*Response // 已收到响应头、还没取正文的请求
	got     []*Response
	seq     uint64
	notify  chan struct{} // 有新响应时关闭并换一个新的，用来唤醒等待者
}

func newCapture(ctx context.Context) *Capture {
	return &Capture{ctx: ctx, pending: map[network.RequestID]*Response{}, notify: make(chan struct{})}
}

// Watch 设置要关心的接口网址片段，比如 "mtop.taobao.idlemtopsearch.pc.search"。
func (c *Capture) Watch(fragments ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watch = append(c.watch, fragments...)
}

// Mark 返回当前的编号。之后用 WaitAfter 只认这个编号之后才收到的响应，避免把上一次操作的响应当成这一次的。
func (c *Capture) Mark() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

func (c *Capture) listen() error {
	// 回调在读取浏览器消息的循环里执行，不能阻塞，也不能在里面发命令；取正文放到单独的 goroutine。
	return chromedp.Listen(c.ctx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventResponseReceived:
			if e.Response == nil || !c.watched(e.Response.URL) {
				return
			}
			r := &Response{URL: e.Response.URL, Status: e.Response.Status, Headers: lowerHeaders(e.Response.Headers)}
			c.mu.Lock()
			c.pending[e.RequestID] = r
			c.mu.Unlock()
		case *network.EventLoadingFinished:
			if r := c.takePending(e.RequestID); r != nil {
				go c.fetchBody(e.RequestID, r)
			}
		case *network.EventLoadingFailed:
			if r := c.takePending(e.RequestID); r != nil {
				r.Err = errors.New(e.ErrorText)
				c.add(r)
			}
		}
	})
}

func (c *Capture) watched(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range c.watch {
		if strings.Contains(url, f) {
			return true
		}
	}
	return false
}

func (c *Capture) takePending(id network.RequestID) *Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.pending[id]
	delete(c.pending, id)
	return r
}

// fetchBody 在响应加载完成之后再取正文；页面跳转后资源会被丢弃，所以要尽快取。
func (c *Capture) fetchBody(id network.RequestID, r *Response) {
	res, err := chromedp.Call(c.ctx, network.GetResponseBody, network.GetResponseBodyParams{RequestID: id})
	if err != nil {
		r.Err = err
	} else {
		r.Body = res.Body
	}
	c.add(r)
}

func (c *Capture) add(r *Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	r.Seq = c.seq
	c.got = append(c.got, r)
	if len(c.got) > maxKept {
		c.got = c.got[len(c.got)-maxKept:]
	}
	close(c.notify)
	c.notify = make(chan struct{})
}

// WaitAfter 等编号大于 mark、网址含 fragment 的第一条响应，最多等 timeout。
func (c *Capture) WaitAfter(ctx context.Context, mark uint64, fragment string, timeout time.Duration) (*Response, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		for _, r := range c.got {
			if r.Seq > mark && strings.Contains(r.URL, fragment) {
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

// LastAfter 返回编号大于 mark、网址含 fragment 的最后一条响应；没有就返回 nil。
// 筛选时页面可能连发几次请求，只有最后一次才是筛选完成后的结果。
func (c *Capture) LastAfter(mark uint64, fragment string) *Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.got) - 1; i >= 0; i-- {
		r := c.got[i]
		if r.Seq <= mark {
			break
		}
		if strings.Contains(r.URL, fragment) {
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
