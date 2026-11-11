// Package resources 按账号创建和关闭浏览器、代理、虚拟屏幕及人工画面。
package resources

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/proxyforward"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

var ErrClosed = errors.New("资源会话已关闭")

type screen interface {
	Name() string
	Close() error
}

type forwarder interface {
	URL() string
	Close() error
}

type Factory struct {
	site          web.Site
	startDisplay  func(context.Context, int, io.Writer) (screen, error)
	startProxy    func(context.Context, *url.URL) (forwarder, error)
	startChrome   func(context.Context, browser.Options) (*browser.Session, error)
	closeChrome   func(*browser.Session)
	newRunner     func(*browser.Session, *pacer.Pacer, pacer.Range, web.Site, web.Pace, web.Budget, web.Logger) *web.Runner
	startRemote   func(context.Context, string, func(context.Context) ([]byte, error), io.Writer) (*remote.Session, error)
	closeRemote   func(*remote.Session) error
	readPage      func(context.Context, *browser.Session) (browser.DiagnosticPage, error)
	chromeVersion func(context.Context, *browser.Session) (string, error)
}

// New 的站点由入口装配，测试传本地假页，不增加用户设置。
func New(site web.Site) (*Factory, error) {
	home, err := url.Parse(site.Home)
	if err != nil || (home.Scheme != "http" && home.Scheme != "https") || home.Hostname() == "" || home.User != nil || home.RawQuery != "" || home.Fragment != "" || !strings.HasSuffix(site.Home, "/") || site.APIHost == "" {
		return nil, errors.New("浏览器站点配置无效")
	}
	return &Factory{
		site: site,
		startDisplay: func(ctx context.Context, id int, output io.Writer) (screen, error) {
			return browser.StartDisplayWithLog(ctx, id, output)
		},
		startProxy: func(ctx context.Context, upstream *url.URL) (forwarder, error) {
			return proxyforward.Start(ctx, upstream)
		},
		startChrome: browser.Start,
		closeChrome: func(chrome *browser.Session) { chrome.Close() },
		newRunner:   web.NewRunner,
		startRemote: remote.StartWithFallbackLog,
		closeRemote: func(view *remote.Session) error { return view.Close() },
		readPage: func(ctx context.Context, chrome *browser.Session) (browser.DiagnosticPage, error) {
			return chrome.DiagnosticPage(ctx)
		},
		chromeVersion: func(ctx context.Context, chrome *browser.Session) (string, error) { return chrome.Version(ctx) },
	}, nil
}

func (f *Factory) Open(ctx context.Context, spec service.ResourceSpec) (_ service.SessionResource, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.AccountID < 1 || spec.AccountID > 12767 || spec.Budget == nil || spec.ProfileDir == "" || spec.CacheMB < 1 {
		return nil, errors.New("浏览器账号、额度、目录或缓存配置无效")
	}
	if _, err := time.LoadLocation(spec.Timezone); spec.Timezone == "" || spec.Timezone == "Local" || err != nil {
		return nil, errors.New("浏览器时区配置无效")
	}
	if spec.Pacer == nil {
		spec.Pacer = pacer.New(nil)
	}
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &resource{factory: f, ctx: life, cancel: cancel, started: time.Now(), outputs: map[string]io.WriteCloser{}}
	stopStartup := context.AfterFunc(ctx, cancel)
	defer stopStartup()
	defer func() {
		if err != nil {
			r.log("资源启动失败", map[string]any{"原因": err.Error()})
			err = errors.Join(err, r.Close())
		}
	}()
	if spec.DiagnosticsDir != "" {
		r.logs, err = diagnostics.Open(spec.DiagnosticsDir, spec.Timezone)
		if err != nil {
			return nil, err
		}
		for _, name := range []string{"chrome", "xvfb", "x11vnc"} {
			r.outputs[name], err = r.logs.ProcessLog(name)
			if err != nil {
				return nil, err
			}
		}
		if err = r.logs.Event("环境", resourceEnvironment(spec)); err != nil {
			return nil, err
		}
	}
	r.display, err = f.startDisplay(life, int(spec.AccountID), r.outputs["xvfb"])
	if err != nil {
		return nil, err
	}
	proxyAddress := ""
	if spec.Proxy != nil {
		upstream := *spec.Proxy
		r.proxy, err = f.startProxy(life, &upstream)
		if err != nil {
			return nil, err
		}
		proxyAddress = r.proxy.URL()
	}
	r.chrome, err = f.startChrome(life, browser.Options{
		AccountID: int(spec.AccountID), ProfileDir: spec.ProfileDir, Proxy: proxyAddress,
		Env: []string{"DISPLAY=" + r.display.Name()}, TimeZone: spec.Timezone, CacheMB: spec.CacheMB,
		Output: r.outputs["chrome"],
	})
	if err != nil {
		return nil, err
	}
	if r.chrome == nil || r.chrome.Capture == nil {
		return nil, errors.New("浏览器没有完成初始化")
	}
	if r.logs != nil {
		version, versionErr := f.chromeVersion(ctx, r.chrome)
		fields := map[string]any{"chrome_version": version}
		if versionErr != nil {
			fields["error"] = versionErr.Error()
		}
		if err = r.logs.Event("浏览器版本", fields); err != nil {
			return nil, err
		}
	}
	r.logSink = spec.Log
	budget := spec.Budget
	if r.logs != nil {
		budget = diagnosticBudget{r: r, next: budget}
	}
	r.runner = f.newRunner(r.chrome, spec.Pacer, spec.Typing, f.site, spec.Pace, budget, r.log)
	if r.logs != nil {
		r.runner.Diagnose = r.diagnose
	}
	stopStartup()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.stopParent = context.AfterFunc(ctx, func() { _ = r.Close() })
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r, nil
}

type resource struct {
	factory   *Factory
	ctx       context.Context
	cancel    context.CancelFunc
	display   screen
	proxy     forwarder
	chrome    *browser.Session
	runner    *web.Runner
	logs      *diagnostics.Recorder
	outputs   map[string]io.WriteCloser
	logSink   web.Logger
	started   time.Time
	phases    []map[string]any
	bundles   []string
	summary   map[string]any
	lastError string

	mu            sync.Mutex
	openMu        sync.Mutex
	closed        bool
	stopParent    func() bool
	remote        *remote.Session
	remoteCancel  context.CancelFunc
	openingCancel context.CancelFunc
	remoteErr     error
	closeOnce     sync.Once
	closeErr      error
}

func (r *resource) Runner() *web.Runner      { return r.runner }
func (r *resource) Chrome() *browser.Session { return r.chrome }

func (r *resource) OpenRemote(ctx context.Context) (*remote.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.openMu.Lock()
	defer r.openMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.closed || r.remoteErr != nil {
		err := errors.Join(ErrClosed, r.remoteErr)
		r.mu.Unlock()
		return nil, err
	}
	if r.remote != nil {
		view := r.remote
		r.mu.Unlock()
		return view, nil
	}
	// 请求取消只中止启动；成功的远程画面跟随资源会话寿命。
	life, cancel := context.WithCancel(r.ctx)
	r.openingCancel = cancel
	r.mu.Unlock()
	stop := context.AfterFunc(ctx, cancel)
	view, err := r.factory.startRemote(life, r.display.Name(), r.chrome.ScreenshotJPEG, r.outputs["x11vnc"])
	stop()
	if err == nil {
		err = ctx.Err()
	}
	r.mu.Lock()
	r.openingCancel = nil
	if r.closed {
		err = errors.Join(err, ErrClosed)
	}
	if view == nil && err == nil {
		err = errors.New("远程画面没有完成启动")
	}
	if err == nil {
		r.remote, r.remoteCancel = view, cancel
		r.mu.Unlock()
		return view, nil
	}
	r.mu.Unlock()
	cancel()
	r.log("远程画面启动失败", map[string]any{"原因": err.Error()})
	if view != nil {
		closeErr := r.factory.closeRemote(view)
		r.mu.Lock()
		r.remoteErr = errors.Join(r.remoteErr, closeErr)
		r.mu.Unlock()
		err = errors.Join(err, closeErr)
	}
	return nil, err
}

func (r *resource) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		stop, openingCancel := r.stopParent, r.openingCancel
		r.mu.Unlock()
		if stop != nil {
			stop()
		}
		if openingCancel != nil {
			openingCancel()
		}
		// 等待可能晚返回的远程启动完成清理，再关闭它依赖的浏览器和显示。
		r.openMu.Lock()
		r.mu.Lock()
		view, remoteCancel, remoteErr := r.remote, r.remoteCancel, r.remoteErr
		r.mu.Unlock()
		r.openMu.Unlock()
		r.closeErr = remoteErr
		if view != nil {
			r.closeErr = errors.Join(r.closeErr, r.factory.closeRemote(view))
		}
		if remoteCancel != nil {
			remoteCancel()
		}
		if r.chrome != nil {
			r.factory.closeChrome(r.chrome)
		}
		if r.proxy != nil {
			r.closeErr = errors.Join(r.closeErr, r.proxy.Close())
		}
		if r.display != nil {
			r.closeErr = errors.Join(r.closeErr, r.display.Close())
		}
		if r.logs != nil {
			r.closeErr = errors.Join(r.closeErr, r.closeDiagnostics())
		}
		r.cancel()
	})
	return r.closeErr
}
