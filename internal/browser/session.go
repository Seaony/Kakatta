package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

type Options struct {
	ExecPath   string
	ProfileDir string
	AccountID  int
	Proxy      string
	Env        []string
	TimeZone   string
	CacheMB    int
	Width      int
	Height     int
	WebGL      string // 仅供实验选择实际渲染后端
	Output     io.Writer
}

type Session struct {
	ctx         context.Context
	cancelTab   context.CancelFunc
	cancelAlloc context.CancelFunc
	watchMu     sync.Mutex
	stopWatch   func() bool
	closeOnce   sync.Once
	cmd         *exec.Cmd
	navigation  *navigationState
	Capture     *Capture
	domReadOnce sync.Once
	domRead     chan struct{}
}

var startMu sync.Mutex

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
	// 分配器在 root 下会自动关沙盒，必须显式阻止。
	chromedp.Flag("no-sandbox", false),
	chromedp.Flag("lang", "zh-CN"),
	chromedp.Flag("hide-crash-restore-bubble", true),
	chromedp.Flag("window-position", "0,0"),
}

// Start 用账号固定的端口启动有界面浏览器，整个配置目录只允许一个进程使用。
func Start(parent context.Context, o Options) (*Session, error) {
	startMu.Lock()
	defer startMu.Unlock()
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if err := normalizeOptions(&o); err != nil {
		return nil, err
	}
	glOptions, glEnv, err := webGLOptions(o.WebGL)
	if err != nil {
		return nil, err
	}
	o.Env = append(slices.Clone(o.Env), glEnv...)
	if err := prepareProfile(o.ProfileDir); err != nil {
		return nil, err
	}
	port := 20000 + o.AccountID
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("账号的调试端口 %d 已被占用：%w", port, err)
	}
	l.Close()
	if o.ExecPath == "" {
		o.ExecPath, err = FindChrome()
		if err != nil {
			return nil, err
		}
	}
	home := filepath.Join(o.ProfileDir, ".runtime")
	crash := filepath.Join(home, "crash")
	if err := os.RemoveAll(crash); err != nil {
		return nil, fmt.Errorf("清理浏览器崩溃目录：%w", err)
	}
	for _, dir := range []string{home, crash, filepath.Join(home, "config")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("创建浏览器运行目录：%w", err)
		}
	}
	opts := append(slices.Clone(keptFlags), glOptions...)
	s := &Session{}
	opts = append(opts,
		chromedp.ExecPath(o.ExecPath),
		chromedp.UserDataDir(o.ProfileDir),
		chromedp.Flag("remote-debugging-port", strconv.Itoa(port)),
		chromedp.Flag("remote-debugging-address", "127.0.0.1"),
		chromedp.Flag("disk-cache-size", strconv.FormatInt(int64(o.CacheMB)<<20, 10)),
		chromedp.Flag("crash-dumps-dir", crash),
		chromedp.WindowSize(o.Width, o.Height),
		// 用本地 XIM 读取键盘映射，避免 GTK 的旧缓存丢字。
		chromedp.Env(append(o.Env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "TZ="+o.TimeZone,
			"GTK_IM_MODULE=xim", "XMODIFIERS=@im=none")...),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			configureProcessGroup(cmd)
			s.cmd = cmd
		}),
		remote.WebSocket,
	)
	if o.Proxy != "" {
		opts = append(opts, chromedp.ProxyServer(o.Proxy))
	}
	if o.Output != nil {
		opts = append(opts, chromedp.CombinedOutput(o.Output))
	}
	// 运行超时要先正常关闭，不能让父上下文直接杀掉浏览器。
	baseCtx, cancelBase := context.WithCancel(context.WithoutCancel(parent))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(baseCtx, opts...)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	s.ctx, s.cancelTab = tabCtx, cancelTab
	s.cancelAlloc = func() { cancelBase(); cancelAlloc() }
	// 这里只取消上下文，等初始化返回后再等分配器退出，避免和它的启动计数竞争。
	stopStartup := context.AfterFunc(parent, cancelBase)
	defer stopStartup()
	s.navigation = newNavigationState()
	if err := chromedp.Listen(tabCtx, s.navigation.event); err != nil {
		s.Close()
		return nil, err
	}
	s.Capture = newCapture(tabCtx)
	if err := s.Capture.listen(); err != nil {
		s.Close()
		return nil, err
	}
	err = initializeTarget(tabCtx)
	if err == nil {
		_, err = chromedp.Call(tabCtx, page.SetLifecycleEventsEnabled, page.SetLifecycleEventsEnabledParams{Enabled: true})
	}
	if err == nil {
		var tree page.GetFrameTreeResult
		tree, err = chromedp.Call(tabCtx, page.GetFrameTree, cdp.Empty{})
		if err == nil && tree.FrameTree != nil && tree.FrameTree.Frame != nil {
			s.Capture.setMainFrame(tree.FrameTree.Frame.ID)
		}
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("启动浏览器失败：%w", err)
	}
	s.watchMu.Lock()
	s.stopWatch = context.AfterFunc(parent, s.Close)
	s.watchMu.Unlock()
	return s, nil
}

func initializeTarget(ctx context.Context) error {
	if err := chromedp.Do(ctx); err != nil {
		return err
	}
	// 驱动在空白页识别目标类型后，不再保留脚本执行上下文的事件。
	_, err := chromedp.Call(ctx, runtime.Disable, cdp.Empty{})
	return err
}

func normalizeOptions(o *Options) error {
	if o.ProfileDir == "" {
		return fmt.Errorf("没有指定浏览器配置目录")
	}
	if o.AccountID == 0 {
		o.AccountID = 1
	}
	if o.AccountID < 1 || o.AccountID > 12767 {
		return fmt.Errorf("账号编号超出调试端口范围")
	}
	var err error
	o.ProfileDir, err = filepath.Abs(o.ProfileDir)
	if err != nil {
		return err
	}
	if o.TimeZone == "" {
		o.TimeZone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(o.TimeZone); err != nil {
		return fmt.Errorf("浏览器时区无效：%w", err)
	}
	if o.CacheMB == 0 {
		o.CacheMB = 256
	}
	if o.CacheMB < 1 {
		return fmt.Errorf("浏览器缓存大小必须为正数")
	}
	if o.Width == 0 {
		o.Width = 1920
	}
	if o.Height == 0 {
		o.Height = 1080
	}
	if o.Width < 1 || o.Height < 1 {
		return fmt.Errorf("浏览器窗口大小必须为正数")
	}
	return nil
}

func prepareProfile(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	active, err := profileInUse(dir)
	if err != nil {
		return fmt.Errorf("检查浏览器配置目录是否占用：%w", err)
	}
	if active {
		return fmt.Errorf("这个账号的浏览器还在运行")
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理浏览器旧锁：%w", err)
		}
	}
	return nil
}

func (s *Session) domReadPermit() chan struct{} {
	s.domReadOnce.Do(func() { s.domRead = make(chan struct{}, 1) })
	return s.domRead
}

func (s *Session) Context() context.Context { return s.ctx }

// Close 先让浏览器写回登录状态，再清理整个进程组里的残留进程。
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.watchMu.Lock()
		if s.stopWatch != nil {
			s.stopWatch()
		}
		s.watchMu.Unlock()
		pid := s.PID()
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		chromedp.Cancel(ctx)
		cancel()
		terminateProcessGroup(pid)
		// 没分配成功时，Cancel 已取走一次性信号，不能再等第二次。
		if chromedp.FromContext(s.ctx).Browser != nil {
			s.cancelTab()
		}
		s.cancelAlloc()
	})
}

// Navigate 保留原调用入口，单次操作应使用 NavigateContext。
func (s *Session) Navigate(url string, timeout time.Duration) error {
	_, err := s.NavigateContext(s.ctx, url, timeout)
	return err
}

// NavigateContext 只等本次主框架加载，取消本次操作不会关掉浏览器。
func (s *Session) NavigateContext(caller context.Context, url string, timeout time.Duration) (string, error) {
	return s.NavigateContextWithStart(caller, url, timeout, nil)
}

// NavigateContextWithStart 在开始提交主框架导航命令时调用 started。
func (s *Session) NavigateContextWithStart(caller context.Context, url string, timeout time.Duration, started func()) (string, error) {
	lifetime, cancelLifetime := s.operationContext(caller)
	defer cancelLifetime()
	ctx, cancelTimeout := context.WithTimeout(lifetime, timeout)
	defer cancelTimeout()
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("打开页面失败：%w", operationError(ctx, err))
	}
	result, err := chromedp.Run(ctx, func(ctx context.Context, t *chromedp.Target) (string, error) {
		events, stop := t.Subscribe(page.LifecycleEvent.Method)
		defer stop()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if started != nil {
			started()
		}
		res, err := cdp.Call(ctx, t, page.Navigate, page.NavigateParams{URL: url})
		if err != nil {
			return "", err
		}
		if res.ErrorText != "" {
			return "", fmt.Errorf("%s", res.ErrorText)
		}
		if res.IsDownload {
			return "", fmt.Errorf("页面跳转成了下载")
		}
		loaded := res.LoaderID == ""
		for !loaded {
			select {
			case raw, ok := <-events:
				if !ok {
					return "", fmt.Errorf("页面事件连接已关闭")
				}
				var event page.EventLifecycleEvent
				if err := json.Unmarshal(raw, &event); err != nil {
					return "", err
				}
				loaded = event.FrameID == res.FrameID && event.LoaderID == res.LoaderID && event.Name == "DOMContentLoaded"
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		tree, err := cdp.Call(ctx, t, page.GetFrameTree, cdp.Empty{})
		if err != nil {
			return "", err
		}
		if tree.FrameTree == nil || tree.FrameTree.Frame == nil {
			return "", fmt.Errorf("读不到主框架网址")
		}
		frame := tree.FrameTree.Frame
		return frame.URL + frame.URLFragment, nil
	})
	if err != nil {
		return "", fmt.Errorf("打开页面失败：%w", operationError(ctx, err))
	}
	return result, nil
}

func (s *Session) URL() (string, error) {
	return s.URLContext(s.ctx)
}

// URLContext 只读当前网址，不让卡住的协议请求挡住运行收尾。
func (s *Session) URLContext(caller context.Context) (string, error) {
	ctx, cancel := s.operationContext(caller)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", operationError(ctx, err)
	}
	res, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return "", operationError(ctx, err)
	}
	if res.FrameTree == nil || res.FrameTree.Frame == nil {
		return "", fmt.Errorf("读不到主框架网址")
	}
	f := res.FrameTree.Frame
	return f.URL + f.URLFragment, nil
}

func (s *Session) operationContext(caller context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(s.ctx)
	if caller.Err() != nil {
		cancel(context.Cause(caller))
	}
	stop := context.AfterFunc(caller, func() { cancel(context.Cause(caller)) })
	return ctx, func() { stop(); cancel(nil) }
}

func (s *Session) readContext(caller context.Context) (context.Context, context.CancelFunc) {
	lifetime, cancelLifetime := s.operationContext(caller)
	ctx, cancelTimeout := context.WithTimeout(lifetime, 5*time.Second)
	return ctx, func() { cancelTimeout(); cancelLifetime() }
}

func operationError(ctx context.Context, err error) error {
	if current := ctx.Err(); current != nil && !errors.Is(err, current) {
		err = errors.Join(err, current)
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) {
		err = errors.Join(err, cause)
	}
	return err
}

// HasCookie 运行前只查 Cookie 名字，不查看值。
func (s *Session) HasCookie(urls []string, name string) (bool, error) {
	return s.HasCookieContext(s.ctx, urls, name)
}

func (s *Session) HasCookieContext(ctx context.Context, urls []string, name string) (bool, error) {
	return s.cookiePresent(ctx, urls, name, false)
}

// LoginCookiePresent 只在判断扫码完成时检查非空，不返回、不保存 Cookie 值。
func (s *Session) LoginCookiePresent(urls []string, name string) (bool, error) {
	return s.LoginCookiePresentContext(s.ctx, urls, name)
}

func (s *Session) LoginCookiePresentContext(ctx context.Context, urls []string, name string) (bool, error) {
	return s.cookiePresent(ctx, urls, name, true)
}

func (s *Session) cookiePresent(caller context.Context, urls []string, name string, requireValue bool) (present bool, err error) {
	ctx, cancel := s.readContext(caller)
	defer cancel()
	defer func() { err = operationError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	res, err := chromedp.Call(ctx, network.GetCookies, network.GetCookiesParams{URLs: urls})
	if err != nil {
		return false, err
	}
	for _, c := range res.Cookies {
		if c.Name == name {
			return !requireValue || c.Value != "", nil
		}
	}
	return false, nil
}

func commandUsesProfile(args []string, dir string) bool {
	for i, arg := range args {
		value, ok := strings.CutPrefix(arg, "--user-data-dir=")
		if !ok && arg == "--user-data-dir" && i+1 < len(args) {
			value, ok = args[i+1], true
		}
		if ok && filepath.Clean(value) == dir {
			return true
		}
	}
	return false
}
