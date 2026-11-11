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
)

type Options struct {
	DataDir  string
	LogDir   string
	Timezone string
	Proxy    *url.URL
	Proxies  []*url.URL
	WebGL    string
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
	data := writable("data_directory", o.DataDir)
	logs := writable("log_directory", o.LogDir)
	zone := timezone(o.Timezone)
	upstreams := append([]*url.URL(nil), o.Proxies...)
	if o.Proxy != nil {
		upstreams = append(upstreams, o.Proxy)
	}
	checks := []Check{data, logs, disk(o.DataDir), zone, proxies(ctx, upstreams)}
	if !data.Passed || !logs.Passed || !zone.Passed {
		for _, id := range []string{"display", "chrome", "remote", "cleanup"} {
			checks = append(checks, Check{ID: id, Detail: "未执行：请先修复目录或时区检查失败项", Action: "修复上面的失败项后重新运行自检"})
		}
		return checks
	}
	return append(checks, browserChecks(ctx, o)...)
}

func proxies(ctx context.Context, upstreams []*url.URL) Check {
	c := Check{ID: "proxy", Passed: true}
	addresses := []string{}
	failed := []string{}
	for _, upstream := range upstreams {
		if upstream == nil {
			failed = append(failed, "代理配置缺失")
			continue
		}
		result := proxy(ctx, upstream)
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
		c.Detail = fmt.Sprintf("%d 个代理中 %d 个检查失败：%s", len(upstreams), len(failed), strings.Join(failed, "；"))
		c.Action = "检查失败的代理进程、地址和端口；这里只验证代理端口，不验证认证或出口"
	case len(upstreams) == 0:
		c.Detail = "未配置账号代理；自检不会访问外部服务"
	default:
		c.Detail = fmt.Sprintf("已连接 %d 个代理端口，未发送请求；认证和出口仍需实际使用时确认", len(upstreams))
	}
	return c
}

func writable(id, dir string) Check {
	c := Check{ID: id, Action: "检查数据挂载及 PUID/PGID，让容器运行用户可读写该目录"}
	if dir == "" {
		c.Detail = "没有指定目录"
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
		c.Detail = "目录写入失败：" + err.Error()
		return c
	}
	c.Passed, c.Detail, c.Action = true, "目录可创建、写入并同步文件", ""
	return c
}

func timezone(name string) Check {
	c := Check{ID: "timezone", Action: "将时区设为有效名称，例如 Asia/Shanghai"}
	if name == "" || name == "Local" {
		c.Detail = "时区需要明确指定，不能使用空值或 Local"
		return c
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		c.Detail = "时区不存在"
		return c
	}
	c.Passed, c.Detail, c.Action = true, "时区有效", ""
	c.Values = map[string]any{"timezone": name, "local_time": time.Now().In(loc).Format(time.RFC3339)}
	return c
}

func proxy(ctx context.Context, upstream *url.URL) Check {
	c := Check{ID: "proxy"}
	if upstream == nil {
		c.Passed, c.Detail = true, "未配置账号代理；自检不会访问外部服务"
		return c
	}
	c.Action = "检查代理进程、地址和端口；这里只验证代理端口，不验证认证或出口"
	if (upstream.Scheme != "http" && upstream.Scheme != "socks5") || upstream.Hostname() == "" {
		c.Detail = "代理地址需要使用 http 或 socks5，并包含主机名"
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
		c.Detail = "无法连接代理端口：" + err.Error()
		return c
	}
	if err := conn.Close(); err != nil {
		c.Detail = "关闭代理自检连接失败：" + err.Error()
		return c
	}
	c.Passed, c.Detail, c.Action = true, "已连接代理端口，未发送请求；认证和出口仍需实际使用时确认", ""
	return c
}

func diskResult(available uint64, err error) Check {
	c := Check{ID: "disk_space", Action: "确认数据挂载正常并清理磁盘空间，再重新运行自检"}
	if err != nil {
		c.Detail = "无法读取剩余磁盘空间：" + err.Error()
		return c
	}
	c.Values = map[string]any{"available_bytes": available}
	if available == 0 {
		c.Detail = "数据目录所在磁盘没有可用空间"
		return c
	}
	c.Passed, c.Detail, c.Action = true, fmt.Sprintf("数据目录所在磁盘可用 %.1f MiB", float64(available)/(1<<20)), ""
	return c
}
