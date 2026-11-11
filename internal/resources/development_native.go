//go:build kakatta_dev

package resources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"runtime"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

// NewDevelopmentNative 只在开发构建里接回环假页，复用正式的账号资源管理。
func NewDevelopmentNative(site web.Site) (*Factory, error) {
	factory, err := New(site)
	if err != nil {
		return nil, err
	}
	home, _ := url.Parse(site.Home)
	if !developmentLoopbackHost(home.Host) || !developmentLoopbackHost(site.APIHost) {
		return nil, errors.New("本机开发只允许数字回环地址的假页面和接口")
	}
	switch runtime.GOOS {
	case "linux":
		return factory, nil
	case "darwin":
		factory.startDisplay = func(ctx context.Context, _ int, _ io.Writer) (screen, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return developmentNativeScreen{}, nil
		}
		factory.startChrome = func(ctx context.Context, options browser.Options) (*browser.Session, error) {
			if options.ExecPath == "" {
				path, err := browser.FindChrome()
				if err != nil {
					return nil, fmt.Errorf("本机假页需要 Google Chrome，请安装后重试：%w", err)
				}
				options.ExecPath = path
			}
			return browser.Start(ctx, options)
		}
		factory.startRemote = func(ctx context.Context, _ string, capture func(context.Context) ([]byte, error), _ io.Writer) (*remote.Session, error) {
			return remote.StartDevelopmentNativePreview(ctx, capture)
		}
		return factory, nil
	default:
		return nil, errors.New("本机假页开发只支持 macOS 和 Linux")
	}
}

func developmentLoopbackHost(authority string) bool {
	u, err := url.Parse("//" + authority)
	if err != nil || u.Host != authority || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	address, err := netip.ParseAddr(u.Hostname())
	return err == nil && address.Zone() == "" && address.IsLoopback()
}

type developmentNativeScreen struct{}

func (developmentNativeScreen) Name() string { return "" }
func (developmentNativeScreen) Close() error { return nil }
