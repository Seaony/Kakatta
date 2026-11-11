//go:build kakatta_dev

package resources

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func TestDevelopmentNativeRejectsNonLoopbackAndMalformedSites(t *testing.T) {
	for _, site := range []web.Site{
		web.Goofish(),
		{Home: "http://192.168.1.2/", APIHost: "127.0.0.1:8080"},
		{Home: "http://127.0.0.1/", APIHost: "h5api.m.goofish.com"},
		{Home: "http://localhost/", APIHost: "127.0.0.1"},
		{Home: "http://127.0.0.1/", APIHost: "localhost"},
		{Home: "http://[::]/", APIHost: "[::1]"},
		{Home: "http://127.0.0.1/", APIHost: "127.0.0.1/path"},
		{Home: "http://127.0.0.1/", APIHost: "127.0.0.1?host=example.invalid"},
		{Home: "http://127.0.0.1/", APIHost: "user:secret@127.0.0.1"},
		{Home: "http://127.0.0.1/", APIHost: "http://127.0.0.1"},
		{Home: "file:///tmp/", APIHost: "127.0.0.1"},
		{Home: "http://user:secret@127.0.0.1/", APIHost: "127.0.0.1"},
		{Home: "http://127.0.0.1/?secret=value", APIHost: "127.0.0.1"},
	} {
		factory, err := NewDevelopmentNative(site)
		if err == nil || factory != nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("非法假站点必须在启动浏览器前拒绝且不回显凭据：%v", err)
		}
	}
}

func TestDevelopmentNativeAcceptsNumericLoopbackSites(t *testing.T) {
	for _, site := range []web.Site{
		{Home: "http://127.0.0.1:18080/", APIHost: "127.0.0.1:18081"},
		{Home: "http://127.20.1.9/", APIHost: "127.1.1.1"},
		{Home: "https://[::1]:18443/", APIHost: "[::1]:18444"},
	} {
		factory, err := NewDevelopmentNative(site)
		if err != nil || factory == nil || factory.site != site {
			t.Fatalf("数字回环假站点应保留原配置：%v", err)
		}
	}
}

func TestDevelopmentNativeMissingExecutableIsClearAndDoesNotOpenPage(t *testing.T) {
	factory, err := NewDevelopmentNative(web.Site{Home: "http://127.0.0.1/", APIHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	chrome, err := factory.startChrome(t.Context(), browser.Options{
		ExecPath: filepath.Join(t.TempDir(), "missing-google-chrome"), ProfileDir: t.TempDir(), AccountID: 12006,
	})
	if err == nil || chrome != nil || !strings.Contains(err.Error(), "missing-google-chrome") {
		t.Fatalf("缺失浏览器必须明确失败，不能换成其他浏览器：%v", err)
	}
}

func TestDevelopmentNativeMissingChromeDoesNotChooseAnotherBrowser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅在 Linux 隔离 PATH，macOS 不改动已安装的浏览器")
	}
	t.Setenv("PATH", t.TempDir())
	factory, err := NewDevelopmentNative(web.Site{Home: "http://127.0.0.1/", APIHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	chrome, err := factory.startChrome(t.Context(), browser.Options{ProfileDir: t.TempDir(), AccountID: 12006})
	if !errors.Is(err, browser.ErrNoChrome) || chrome != nil || !strings.Contains(err.Error(), "Google Chrome") {
		t.Fatal("没有 Chrome 时必须明确拒绝，不能改用其他浏览器", err)
	}
}

func TestDevelopmentNativeMacKeepsResourceLifecycleAndReadOnlyRemote(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("只有 macOS 使用原生屏幕和只读画面，Linux 继续使用正式工厂")
	}
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "显式关闭", true: "父上下文取消"}[canceled], func(t *testing.T) {
			factory, err := NewDevelopmentNative(web.Site{Home: "http://127.0.0.1/", APIHost: "127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			closed := make(chan struct{})
			var options browser.Options
			factory.startChrome = func(_ context.Context, got browser.Options) (*browser.Session, error) {
				options = got
				return &browser.Session{Capture: &browser.Capture{}}, nil
			}
			factory.closeChrome = func(*browser.Session) { close(closed) }
			factory.newRunner = func(chrome *browser.Session, p *pacer.Pacer, _ pacer.Range, site web.Site, pace web.Pace, budget web.Budget, log web.Logger) *web.Runner {
				return &web.Runner{S: chrome, P: p, Site: site, Pace: pace, Budget: budget, Log: log}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			spec := service.ResourceSpec{AccountID: 12007, ProfileDir: t.TempDir(), Timezone: "UTC", CacheMB: 128, Budget: testBudget{}}
			opened, err := factory.Open(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opened.Close() })
			if len(options.Env) != 1 || options.Env[0] != "DISPLAY=" || options.AccountID != int(spec.AccountID) || options.ProfileDir != spec.ProfileDir || options.TimeZone != spec.Timezone || options.CacheMB != spec.CacheMB || opened.Runner().Budget != spec.Budget {
				t.Fatal("本机适配改变了账号配置、额度或使用了虚拟屏幕")
			}
			view, err := opened.OpenRemote(t.Context())
			if err != nil || view.Address() != "" || view.HasViewer() {
				t.Fatal("本机只读画面不能提前截图或开放远程输入端口", err)
			}
			if canceled {
				cancel()
			} else if err := opened.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("原生浏览器没有跟随资源会话回收")
			}
			if err := opened.Close(); err != nil {
				t.Fatal("重复关闭必须等待同一次回收", err)
			}
			if _, err := opened.OpenRemote(t.Context()); !errors.Is(err, ErrClosed) {
				t.Fatal("关闭后仍能获取画面", err)
			}
		})
	}
}

func TestDevelopmentNativeLocalChromeUsesSeparateProfileAndCloses(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("KAKATTA_NATIVE_BROWSER_TESTS") != "1" {
		t.Skip("本机独立 Chrome 假页测试需显式开启")
	}
	buildDir, err := filepath.Abs(filepath.Join("..", "..", "build"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(buildDir, 0700); err != nil {
		t.Fatal(err)
	}
	fakeSite := fake.Start()
	t.Cleanup(fakeSite.Close)
	factory, err := NewDevelopmentNative(web.Site{Home: fakeSite.Home(), APIHost: fakeSite.APIHost()})
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelParent := range []bool{false, true} {
		profile, err := os.MkdirTemp(buildDir, "_dev-native-browser.")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(profile) })
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		t.Cleanup(cancel)
		fast := pacer.Between(time.Millisecond, time.Millisecond)
		opened, err := factory.Open(ctx, service.ResourceSpec{
			AccountID: 12007, ProfileDir: profile, Timezone: "UTC", CacheMB: 128, Budget: testBudget{},
			Typing: fast, Pace: web.Pace{HomeStay: fast, Action: fast, PageWait: fast, PreDetail: fast, Detail: fast},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })
		if _, err := opened.Chrome().NavigateContext(ctx, fakeSite.Home(), 5*time.Second); err != nil {
			t.Fatal("本机 Chrome 没有打开回环假页", err)
		}
		if cancelParent {
			cancel()
			select {
			case <-opened.(*resource).ctx.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("父上下文取消后没有收回浏览器")
			}
		} else if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:32007")
		if err != nil {
			t.Fatal("关闭后固定调试端口仍被占用", err)
		}
		listener.Close()
	}
}
