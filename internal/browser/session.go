package browser

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Options 是启动一次浏览器需要的设置。
type Options struct {
	ExecPath   string   // Chrome 或 Edge 的路径，空时自动查找
	ProfileDir string   // 这个闲鱼账号自己的浏览器配置目录，长期保留
	Proxy      string   // 这个账号绑定的代理，空表示直连
	Env        []string // 额外的环境变量，比如 Xvfb 的 DISPLAY
	Width      int      // 窗口宽度，0 表示用浏览器默认值
	Height     int
}

// Session 是一个正在运行的浏览器，以及它的第一个标签页。
type Session struct {
	ctx         context.Context
	cancelTab   context.CancelFunc
	cancelAlloc context.CancelFunc
	Capture     *Capture
}

// keptFlags 是 chromedp 默认参数里页面读不到、只影响浏览器自身杂事的那部分。
// headless、enable-automation、disable-extensions 等会改变行为或本身就是自动化特征的参数一律不加（CRAWL-POLICY 第二节）。
var keptFlags = []chromedp.ExecAllocatorOption{
	chromedp.NoFirstRun,
	chromedp.NoDefaultBrowserCheck,
	chromedp.Flag("disable-background-networking", true),
	chromedp.Flag("disable-breakpad", true),
	chromedp.Flag("disable-sync", true),
	chromedp.Flag("disable-default-apps", true),
	chromedp.Flag("disable-client-side-phishing-detection", true),
	chromedp.Flag("metrics-recording-only", true),
	chromedp.Flag("safebrowsing-disable-auto-update", true),
	chromedp.Flag("password-store", "basic"),
	chromedp.Flag("use-mock-keychain", true),
	chromedp.Flag("disable-hang-monitor", true),
	chromedp.Flag("disable-prompt-on-repost", true),
	chromedp.Flag("disable-dev-shm-usage", true),
}

// Start 启动浏览器并连上它的第一个标签页。
// 用固定端口、只绑定 127.0.0.1 连接，不用 pipe 也不用端口 0：
// Chromium 只在这两种方式下把 navigator.webdriver 置为 true（content/child/runtime_features.cc）。
func Start(parent context.Context, o Options) (*Session, error) {
	if o.ProfileDir == "" {
		return nil, fmt.Errorf("没有指定浏览器配置目录")
	}
	execPath := o.ExecPath
	if execPath == "" {
		p, err := FindChrome()
		if err != nil {
			return nil, err
		}
		execPath = p
	}
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("找不到可用的本机端口：%w", err)
	}

	opts := slices.Clone(keptFlags)
	opts = append(opts,
		chromedp.ExecPath(execPath),
		chromedp.UserDataDir(o.ProfileDir),
		chromedp.Flag("remote-debugging-port", strconv.Itoa(port)),
		chromedp.Flag("remote-debugging-address", "127.0.0.1"),
		remote.WebSocket,
	)
	if o.Proxy != "" {
		opts = append(opts, chromedp.ProxyServer(o.Proxy))
	}
	if len(o.Env) > 0 {
		opts = append(opts, chromedp.Env(o.Env...))
	}
	if o.Width > 0 && o.Height > 0 {
		opts = append(opts, chromedp.WindowSize(o.Width, o.Height))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	s := &Session{ctx: tabCtx, cancelTab: cancelTab, cancelAlloc: cancelAlloc}

	// 先挂上响应监听，再真正启动浏览器，保证第一个请求的响应也能收到。
	s.Capture = newCapture(tabCtx)
	if err := s.Capture.listen(); err != nil {
		s.Close()
		return nil, err
	}
	if err := chromedp.Do(tabCtx); err != nil {
		s.Close()
		return nil, fmt.Errorf("启动浏览器失败：%w", err)
	}
	return s, nil
}

// Context 返回标签页的上下文，给流程代码调用 chromedp 用。
func (s *Session) Context() context.Context { return s.ctx }

// Close 关闭浏览器。登录状态保存在配置目录里，下次启动还在。
func (s *Session) Close() {
	s.cancelTab()
	s.cancelAlloc()
}

// Navigate 在当前标签页打开网址，等到 DOMContentLoaded 就返回（和上游一样，不等图片等资源全部加载完）。
func (s *Session) Navigate(url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	var navErr string
	open := func(ctx context.Context, t *chromedp.Target) (page.NavigateResult, error) {
		res, err := cdp.Call(ctx, t, page.Navigate, page.NavigateParams{URL: url})
		navErr = res.ErrorText
		return res, err
	}
	_, err := chromedp.Run(ctx, chromedp.WaitEvent(page.DomContentEventFired,
		func(page.EventDomContentEventFired) bool { return true }, open))
	if navErr != "" {
		return fmt.Errorf("打开页面失败：%s", navErr)
	}
	if err != nil {
		return fmt.Errorf("打开页面失败：%w", err)
	}
	return nil
}

// URL 返回当前页面的网址。
func (s *Session) URL() (string, error) {
	return chromedp.Run(s.ctx, chromedp.Location())
}

// HasCookie 只检查某个 Cookie 在不在，不返回它的值：应用不能读取、记录或打印 Cookie。
func (s *Session) HasCookie(urls []string, name string) (bool, error) {
	res, err := chromedp.Call(s.ctx, network.GetCookies, network.GetCookiesParams{URLs: urls})
	if err != nil {
		return false, err
	}
	for _, c := range res.Cookies {
		if c.Name == name && c.Value != "" {
			return true, nil
		}
	}
	return false, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
