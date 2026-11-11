// Package preflight 在扫码前检查本机环境，不访问闲鱼。
package preflight

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
)

type Options struct {
	DataDir  string
	LogDir   string
	Timezone string
	Proxy    *url.URL
	Proxies  []*url.URL
	WebGL    string
	Language locales.Language
}

type Check struct {
	ID     string         `json:"id"`
	Passed bool           `json:"passed"`
	Detail string         `json:"detail"`
	Action string         `json:"action,omitempty"`
	Values map[string]any `json:"values,omitempty"`
}

func Run(ctx context.Context, o Options) []Check {
	if o.LogDir == "" && o.DataDir != "" {
		o.LogDir = filepath.Join(o.DataDir, "logs")
	}
	data := writable("data_directory", o.DataDir, o.Language)
	logs := writable("log_directory", o.LogDir, o.Language)
	zone := timezone(o.Timezone, o.Language)
	upstreams := append([]*url.URL(nil), o.Proxies...)
	if o.Proxy != nil {
		upstreams = append(upstreams, o.Proxy)
	}
	checks := []Check{data, logs, disk(o.DataDir, o.Language), zone, proxies(ctx, upstreams, o.Language)}
	if !data.Passed || !logs.Passed || !zone.Passed {
		for _, id := range []string{"display", "chrome", "remote", "cleanup"} {
			checks = append(checks, Check{ID: id, Detail: text(o.Language, "setup_skipped", nil), Action: text(o.Language, "setup_remedy", nil)})
		}
		return checks
	}
	return append(checks, browserChecks(ctx, o)...)
}

func proxies(ctx context.Context, upstreams []*url.URL, language locales.Language) Check {
	c := Check{ID: "proxy", Passed: true}
	addresses := []string{}
	failed := []string{}
	for _, upstream := range upstreams {
		if upstream == nil {
			failed = append(failed, text(language, "proxy_missing", nil))
			continue
		}
		result := proxy(ctx, upstream, language)
		if address, ok := result.Values["address"].(string); ok {
			addresses = append(addresses, address)
		}
		if !result.Passed {
			failed = append(failed, result.Detail)
		}
	}
	c.Values = map[string]any{"count": len(upstreams), "addresses": addresses}
	switch {
	case len(failed) > 0:
		c.Passed = false
		c.Detail = text(language, "proxies_failed", map[string]any{"count": len(upstreams), "failed": len(failed), "errors": strings.Join(failed, text(language, "failure_separator", nil))})
		c.Action = text(language, "proxies_remedy", nil)
	case len(upstreams) == 0:
		c.Detail = text(language, "proxy_unconfigured", nil)
	default:
		c.Detail = text(language, "proxies_connected", map[string]any{"count": len(upstreams)})
	}
	return c
}

func writable(id, dir string, language locales.Language) Check {
	c := Check{ID: id, Action: text(language, "directory_remedy", nil)}
	if dir == "" {
		c.Detail = text(language, "directory_unspecified", nil)
		return c
	}
	err := os.MkdirAll(dir, 0700)
	if err == nil {
		var f *os.File
		f, err = os.CreateTemp(dir, ".preflight-write-")
		if err == nil {
			defer os.Remove(f.Name())
			_, err = f.WriteString("kakatta\n")
			if err == nil {
				err = f.Sync()
			}
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		c.Detail = text(language, "directory_failed", map[string]any{"error": err.Error()})
		return c
	}
	c.Passed, c.Detail, c.Action = true, text(language, "directory_writable", nil), ""
	return c
}

func timezone(name string, language locales.Language) Check {
	c := Check{ID: "timezone", Action: text(language, "timezone_remedy", nil)}
	if name == "" || name == "Local" {
		c.Detail = text(language, "timezone_unspecified", nil)
		return c
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		c.Detail = text(language, "timezone_unknown", nil)
		return c
	}
	c.Passed, c.Detail, c.Action = true, text(language, "timezone_valid", nil), ""
	c.Values = map[string]any{"timezone": name, "local_time": time.Now().In(loc).Format(time.RFC3339)}
	return c
}

func proxy(ctx context.Context, upstream *url.URL, language locales.Language) Check {
	c := Check{ID: "proxy"}
	if upstream == nil {
		c.Passed, c.Detail = true, text(language, "proxy_unconfigured", nil)
		return c
	}
	c.Action = text(language, "proxy_remedy", nil)
	if (upstream.Scheme != "http" && upstream.Scheme != "socks5") || upstream.Hostname() == "" {
		c.Detail = text(language, "proxy_invalid", nil)
		return c
	}
	port := upstream.Port()
	if port == "" {
		port = "80"
		if upstream.Scheme == "socks5" {
			port = "1080"
		}
	}
	address := net.JoinHostPort(upstream.Hostname(), port)
	c.Values = map[string]any{"address": upstream.Scheme + "://" + address}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		c.Detail = text(language, "proxy_connect_failed", map[string]any{"error": err.Error()})
		return c
	}
	if err := conn.Close(); err != nil {
		c.Detail = text(language, "proxy_close_failed", map[string]any{"error": err.Error()})
		return c
	}
	c.Passed, c.Detail, c.Action = true, text(language, "proxy_connected", nil), ""
	return c
}

func diskResult(available uint64, err error, language locales.Language) Check {
	c := Check{ID: "disk_space", Action: text(language, "disk_remedy", nil)}
	if err != nil {
		c.Detail = text(language, "disk_read_failed", map[string]any{"error": err.Error()})
		return c
	}
	c.Values = map[string]any{"available_bytes": available}
	if available == 0 {
		c.Detail = text(language, "disk_full", nil)
		return c
	}
	c.Passed, c.Detail, c.Action = true, text(language, "disk_available", map[string]any{"available": fmt.Sprintf("%.1f", float64(available)/(1<<20))}), ""
	return c
}

func text(language locales.Language, code string, args map[string]any) string {
	if language == "" {
		language = locales.ZhCN
	}
	value, _ := locales.Format(language, locales.Message{Code: "preflight." + code, Args: args})
	return value
}
