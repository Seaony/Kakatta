package server

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Seaony/Kakatta/internal/settings"
)

func TestStaticMarksPublicPlainHTTP(t *testing.T) {
	f := newServerFixture(t, false)
	f.server.web = fstest.MapFS{
		"index.html":  {Data: []byte(`<!doctype html><html data-kakatta-public-http="false"><title>Kakatta</title></html>`)},
		"assets/a.js": {Data: []byte(`export const marker = 'data-kakatta-public-http="false"';`)},
	}
	if _, err := f.settings.Apply(t.Context(), settings.Values{"security.trusted_proxies": json.RawMessage(`["10.0.0.2"]`)}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, path, peer, forwardedFor, proto string
		tls, want                             bool
	}{
		{name: "公网 IPv4 的初始化页", path: "/setup", peer: "203.0.113.9:3456", want: true},
		{name: "公网 IPv6 的登录后页面", path: "/results", peer: "[2001:db8::9]:3456", want: true},
		{name: "公开静态资源保持原样", path: "/assets/a.js", peer: "203.0.113.9:3456"},
		{name: "公网直连 TLS", path: "/login", peer: "203.0.113.9:3456", tls: true},
		{name: "可信代理转发公网明文", path: "/login", peer: "10.0.0.2:3456", forwardedFor: "203.0.113.9", proto: "http", want: true},
		{name: "可信代理终止 TLS", path: "/login", peer: "10.0.0.2:3456", forwardedFor: "203.0.113.9", proto: "https"},
		{name: "可信代理转发私网明文", path: "/login", peer: "10.0.0.2:3456", forwardedFor: "10.0.0.9", proto: "http"},
		{name: "不可信来源的代理头不隐藏提示", path: "/login", peer: "203.0.113.9:3456", forwardedFor: "10.0.0.9", proto: "https", want: true},
		{name: "IPv4 私网", path: "/login", peer: "192.168.0.9:3456"},
		{name: "IPv6 私网", path: "/login", peer: "[fd12::9]:3456"},
		{name: "回环", path: "/login", peer: "127.0.0.1:3456"},
		{name: "共享地址空间", path: "/login", peer: "100.64.0.9:3456"},
	} {
		t.Run(item.name, func(t *testing.T) {
			result := f.request(http.MethodGet, item.path, "", false, func(r *http.Request) {
				r.RemoteAddr = item.peer
				r.Header.Set("Accept", "text/html")
				if item.forwardedFor != "" {
					r.Header.Set("X-Forwarded-For", item.forwardedFor)
					r.Header.Set("X-Forwarded-Proto", item.proto)
				}
				if item.tls {
					r.TLS = &tls.ConnectionState{}
				}
			})
			requireStatus(t, result, http.StatusOK)
			if result.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("不能缓存因连接来源而异的页面")
			}
			if item.path != "/assets/a.js" && (result.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" || result.Header().Get("X-Frame-Options") != "DENY") {
				t.Fatal("网页入口必须拒绝被嵌入")
			}
			marker := `data-kakatta-public-http="false"`
			if item.want {
				marker = `data-kakatta-public-http="true"`
			}
			if !strings.Contains(result.Body.String(), marker) {
				t.Fatalf("页面缺少标记 %s：%s", marker, result.Body.String())
			}
		})
	}
}
