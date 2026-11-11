package resources

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type testBudget struct{}

func (testBudget) Acquire(quota.Kind, string) error { return nil }

type calls struct {
	mu   sync.Mutex
	list []string
}

func (c *calls) add(name string) {
	c.mu.Lock()
	c.list = append(c.list, name)
	c.mu.Unlock()
}

func (c *calls) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.list...)
}

type testScreen struct {
	calls *calls
	err   error
}

func (*testScreen) Name() string { return ":107" }
func (s *testScreen) Close() error {
	s.calls.add("close_display")
	return s.err
}

type testProxy struct {
	calls *calls
	err   error
}

func (*testProxy) URL() string { return "http://127.0.0.1:18888" }
func (p *testProxy) Close() error {
	p.calls.add("close_proxy")
	return p.err
}

func testFactory(t *testing.T) (*Factory, *calls, service.ResourceSpec) {
	t.Helper()
	f, err := New(web.Site{Home: "http://127.0.0.1/", APIHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	c := &calls{}
	f.startDisplay = func(context.Context, int) (screen, error) {
		c.add("open_display")
		return &testScreen{calls: c}, nil
	}
	f.startProxy = func(context.Context, *url.URL) (forwarder, error) {
		c.add("open_proxy")
		return &testProxy{calls: c}, nil
	}
	f.startChrome = func(context.Context, browser.Options) (*browser.Session, error) {
		c.add("open_chrome")
		return &browser.Session{Capture: &browser.Capture{}}, nil
	}
	f.closeChrome = func(*browser.Session) { c.add("close_chrome") }
	f.newRunner = func(chrome *browser.Session, p *pacer.Pacer, _ pacer.Range, site web.Site, pace web.Pace, budget web.Budget, log web.Logger) *web.Runner {
		return &web.Runner{S: chrome, P: p, Site: site, Pace: pace, Budget: budget, Log: log}
	}
	f.startRemote = func(context.Context, string, func(context.Context) ([]byte, error)) (*remote.Session, error) {
		c.add("open_remote")
		return &remote.Session{}, nil
	}
	f.closeRemote = func(*remote.Session) error { c.add("close_remote"); return nil }
	proxy, _ := url.Parse("http://user:password@127.0.0.1:8080")
	spec := service.ResourceSpec{
		AccountID: 7, ProfileDir: t.TempDir(), Proxy: proxy, Timezone: "Asia/Shanghai", CacheMB: 256,
		Pacer: pacer.New(nil), Budget: testBudget{}, Typing: pacer.Between(time.Millisecond, time.Millisecond),
	}
	return f, c, spec
}

func TestOpenRejectsInvalidSpecsBeforeResources(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*service.ResourceSpec)
	}{
		{"zero_account", func(s *service.ResourceSpec) { s.AccountID = 0 }},
		{"negative_account", func(s *service.ResourceSpec) { s.AccountID = -1 }},
		{"large_account", func(s *service.ResourceSpec) { s.AccountID = 12768 }},
		{"missing_budget", func(s *service.ResourceSpec) { s.Budget = nil }},
		{"missing_profile", func(s *service.ResourceSpec) { s.ProfileDir = "" }},
		{"bad_timezone", func(s *service.ResourceSpec) { s.Timezone = "bad/zone" }},
		{"bad_cache", func(s *service.ResourceSpec) { s.CacheMB = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, c, spec := testFactory(t)
			test.change(&spec)
			resource, err := f.Open(t.Context(), spec)
			if err == nil || resource != nil || len(c.snapshot()) != 0 {
				t.Fatalf("非法输入启动了资源：%v %v %v", resource, err, c.snapshot())
			}
		})
	}
	f, c, spec := testFactory(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if resource, err := f.Open(ctx, spec); !errors.Is(err, context.Canceled) || resource != nil || len(c.snapshot()) != 0 {
		t.Fatalf("取消后仍启动资源：%v %v", err, c.snapshot())
	}
}

func TestOpenUsesSpecAndRemoteIsOnDemand(t *testing.T) {
	f, c, spec := testFactory(t)
	var got browser.Options
	f.startChrome = func(_ context.Context, options browser.Options) (*browser.Session, error) {
		c.add("open_chrome")
		got = options
		return &browser.Session{Capture: &browser.Capture{}}, nil
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if r.Runner().S != r.Chrome() || r.Runner().Budget != spec.Budget || r.Runner().P != spec.Pacer {
		t.Fatal("Runner 没有使用同一页面、额度或随机等待")
	}
	if got.AccountID != 7 || got.ProfileDir != spec.ProfileDir || got.TimeZone != spec.Timezone || got.CacheMB != 256 || got.WebGL != "" || got.Proxy != "http://127.0.0.1:18888" || !reflect.DeepEqual(got.Env, []string{"DISPLAY=:107"}) {
		t.Fatal("没有把固定账号、代理、时区、缓存和显示传给浏览器")
	}
	if want := []string{"open_display", "open_proxy", "open_chrome"}; !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("提前启动了远程画面：%v", c.snapshot())
	}
	first, err := r.OpenRemote(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.OpenRemote(t.Context())
	if err != nil || first != second {
		t.Fatal("重复打开没有复用远程画面", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"open_display", "open_proxy", "open_chrome", "open_remote", "close_remote", "close_chrome", "close_proxy", "close_display"}
	if !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("关闭顺序或次数不符：%v", c.snapshot())
	}
	if _, err := r.OpenRemote(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后仍可打开远程画面：%v", err)
	}
}

func TestOpenCleansPartialStarts(t *testing.T) {
	failure := errors.New("启动失败")
	for _, stage := range []string{"display", "proxy", "chrome", "cancel_after_chrome"} {
		t.Run(stage, func(t *testing.T) {
			f, c, spec := testFactory(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := []string{"open_display", "close_display"}
			switch stage {
			case "display":
				f.startDisplay = func(context.Context, int) (screen, error) {
					c.add("open_display")
					return &testScreen{calls: c}, failure
				}
			case "proxy":
				f.startProxy = func(context.Context, *url.URL) (forwarder, error) {
					c.add("open_proxy")
					return &testProxy{calls: c}, failure
				}
				want = []string{"open_display", "open_proxy", "close_proxy", "close_display"}
			default:
				f.startChrome = func(context.Context, browser.Options) (*browser.Session, error) {
					c.add("open_chrome")
					if stage == "cancel_after_chrome" {
						cancel()
						return &browser.Session{Capture: &browser.Capture{}}, nil
					}
					return &browser.Session{}, failure
				}
				want = []string{"open_display", "open_proxy", "open_chrome", "close_chrome", "close_proxy", "close_display"}
			}
			r, err := f.Open(ctx, spec)
			if r != nil || err == nil || !reflect.DeepEqual(c.snapshot(), want) {
				t.Fatalf("启动失败没有清理部分资源：%v %v", err, c.snapshot())
			}
			if stage == "cancel_after_chrome" && !errors.Is(err, context.Canceled) {
				t.Fatal("丢失取消原因", err)
			}
		})
	}
}

func TestRemoteOutlivesOpeningContext(t *testing.T) {
	f, c, spec := testFactory(t)
	var remoteContext context.Context
	f.startRemote = func(ctx context.Context, display string, _ func(context.Context) ([]byte, error)) (*remote.Session, error) {
		remoteContext = ctx
		if display != ":107" {
			t.Fatal("远程画面接了别的账号显示")
		}
		c.add("open_remote")
		return &remote.Session{}, nil
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := r.OpenRemote(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if remoteContext.Err() != nil {
		t.Fatal("HTTP 请求结束关闭了已接纳的远程画面")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if remoteContext.Err() == nil {
		t.Fatal("会话结束没有取消远程画面寿命")
	}
}

func TestCloseAttemptsAllResourcesAndIsIdempotent(t *testing.T) {
	f, c, spec := testFactory(t)
	displayErr, proxyErr, remoteErr := errors.New("屏幕关闭失败"), errors.New("代理关闭失败"), errors.New("远程关闭失败")
	f.startDisplay = func(context.Context, int) (screen, error) { return &testScreen{calls: c, err: displayErr}, nil }
	f.startProxy = func(context.Context, *url.URL) (forwarder, error) { return &testProxy{calls: c, err: proxyErr}, nil }
	f.closeRemote = func(*remote.Session) error { c.add("close_remote"); return remoteErr }
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenRemote(t.Context()); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			err := r.Close()
			if !errors.Is(err, displayErr) || !errors.Is(err, proxyErr) || !errors.Is(err, remoteErr) {
				t.Error("没有返回全部关闭错误", err)
			}
		})
	}
	workers.Wait()
	want := []string{"open_chrome", "open_remote", "close_remote", "close_chrome", "close_proxy", "close_display"}
	if !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("关闭提前中断或执行多次：%v", c.snapshot())
	}
}

func TestCloseCancelsAndJoinsRemoteStartup(t *testing.T) {
	f, c, spec := testFactory(t)
	entered := make(chan struct{})
	f.startRemote = func(ctx context.Context, _ string, _ func(context.Context) ([]byte, error)) (*remote.Session, error) {
		c.add("open_remote")
		close(entered)
		<-ctx.Done()
		return &remote.Session{}, nil
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan error, 1)
	go func() { _, err := r.OpenRemote(t.Context()); opened <- err }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭没有取消并等待远程启动结束")
	}
	if err := <-opened; err == nil {
		t.Fatal("会话关闭后仍接纳了晚返回的远程画面")
	}
	want := []string{"open_display", "open_proxy", "open_chrome", "open_remote", "close_remote", "close_chrome", "close_proxy", "close_display"}
	if !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("晚返回的资源未在 Chrome 之前关闭：%v", c.snapshot())
	}
}

func TestConcurrentRemoteOpenStartsOnlyOnce(t *testing.T) {
	f, c, spec := testFactory(t)
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	var workers sync.WaitGroup
	for range 24 {
		workers.Go(func() {
			if _, err := r.OpenRemote(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	want := []string{"open_display", "open_proxy", "open_chrome", "open_remote"}
	if !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("并发打开重复启动远程画面：%v", c.snapshot())
	}
}

func TestRemoteFailureCleansPartialViewAndAllowsRetry(t *testing.T) {
	f, c, spec := testFactory(t)
	failure := errors.New("远程启动失败")
	attempts := 0
	f.startRemote = func(context.Context, string, func(context.Context) ([]byte, error)) (*remote.Session, error) {
		c.add("open_remote")
		attempts++
		if attempts == 1 {
			return &remote.Session{}, failure
		}
		return &remote.Session{}, nil
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if view, err := r.OpenRemote(t.Context()); !errors.Is(err, failure) || view != nil {
		t.Fatalf("远程启动失败被当成成功：%v", err)
	}
	if _, err := r.OpenRemote(t.Context()); err != nil {
		t.Fatal("远程启动失败后不能在原会话重开", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"open_display", "open_proxy", "open_chrome", "open_remote", "close_remote", "open_remote", "close_remote", "close_chrome", "close_proxy", "close_display"}
	if !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("失败的远程资源未清理或浏览器被提前关闭：%v", c.snapshot())
	}
}

func TestSessionContextCancellationClosesAll(t *testing.T) {
	f, c, spec := testFactory(t)
	ctx, cancel := context.WithCancel(t.Context())
	var chromeLife context.Context
	f.startChrome = func(ctx context.Context, _ browser.Options) (*browser.Session, error) {
		chromeLife = ctx
		c.add("open_chrome")
		return &browser.Session{Capture: &browser.Capture{}}, nil
	}
	r, err := f.Open(ctx, spec)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if _, err := r.OpenRemote(t.Context()); err != nil {
		cancel()
		r.Close()
		t.Fatal(err)
	}
	cancel()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if chromeLife.Err() == nil {
		t.Fatal("会话取消后资源寿命未结束")
	}
	want := []string{"open_display", "open_proxy", "open_chrome", "open_remote", "close_remote", "close_chrome", "close_proxy", "close_display"}
	if !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("会话取消后清理不完整：%v", c.snapshot())
	}
}

func TestNoProxyDoesNotCreateForwarder(t *testing.T) {
	f, c, spec := testFactory(t)
	spec.Proxy = nil
	f.startChrome = func(_ context.Context, options browser.Options) (*browser.Session, error) {
		if options.Proxy != "" {
			t.Fatal("未绑定代理却给浏览器配置了代理")
		}
		c.add("open_chrome")
		return &browser.Session{Capture: &browser.Capture{}}, nil
	}
	r, err := f.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"open_display", "open_chrome", "close_chrome", "close_display"}
	if !reflect.DeepEqual(c.snapshot(), want) {
		t.Fatalf("未绑定代理仍启动了转发器：%v", c.snapshot())
	}
}
