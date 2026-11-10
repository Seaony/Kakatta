package server

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestResolvePeer(t *testing.T) {
	tests := []struct {
		name    string
		remote  string
		trusted []string
		headers http.Header
		tls     bool
		ip      string
		host    string
		secure  bool
	}{
		{name: "直连 IPv4", remote: "198.51.100.8:1234", ip: "198.51.100.8"},
		{name: "直连 IPv6", remote: "[2001:db8::8]:1234", ip: "2001:db8::8"},
		{name: "映射地址规整", remote: "[::ffff:198.51.100.8]:1234", ip: "198.51.100.8"},
		{name: "连接区域标识去掉", remote: "[fe80::8%en0]:1234", ip: "fe80::8"},
		{name: "直接 TLS", remote: "198.51.100.8:1234", tls: true, ip: "198.51.100.8", secure: true},
		{
			name: "不可信连接完全忽略伪造头", remote: "198.51.100.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{
				"X-Forwarded-For":   {"invalid", "192.0.2.8"},
				"X-Forwarded-Proto": {"https", "ftp"},
				"X-Forwarded-Host":  {"attacker.test/bad", "other.test"},
				"Forwarded":         {"for=not-an-address;proto=https;host=attacker.test"},
			},
			ip: "198.51.100.8",
		},
		{
			name: "精确可信地址", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.8"},
			headers: http.Header{"X-Forwarded-For": {"198.51.100.8"}}, ip: "198.51.100.8",
		},
		{
			name: "多层代理从右向左", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"X-Forwarded-For": {"198.51.100.8, 10.0.0.9, 10.0.0.10"}}, ip: "198.51.100.8",
		},
		{
			name: "不可信边界挡住左侧伪造", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"X-Forwarded-For": {"192.0.2.99, 198.51.100.8, 10.0.0.9"}}, ip: "198.51.100.8",
		},
		{
			name: "全部可信取最左地址", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"X-Forwarded-For": {"10.0.0.11, 10.0.0.9"}}, ip: "10.0.0.11",
		},
		{
			name: "头缺失保留连接来源", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"}, ip: "10.0.0.8",
		},
		{
			name: "可信 IPv6 网段", remote: "[2001:db8::8]:1234", trusted: []string{"2001:db8::/32"},
			headers: http.Header{"X-Forwarded-For": {"2001:db9::8, 2001:db8::9"}}, ip: "2001:db9::8",
		},
		{
			name: "映射连接匹配 IPv4 网段", remote: "[::ffff:10.0.0.8]:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"X-Forwarded-For": {"::ffff:198.51.100.8, ::ffff:10.0.0.9"}}, ip: "198.51.100.8",
		},
		{
			name: "映射可信地址匹配 IPv4 连接", remote: "10.0.0.8:1234", trusted: []string{"::ffff:10.0.0.8"},
			headers: http.Header{"X-Forwarded-For": {"198.51.100.8"}}, ip: "198.51.100.8",
		},
		{
			name: "映射可信网段匹配 IPv4 连接", remote: "10.0.0.8:1234", trusted: []string{"::ffff:10.0.0.42/120"},
			headers: http.Header{"X-Forwarded-For": {"198.51.100.8"}}, ip: "198.51.100.8",
		},
		{
			name: "非网络起点规整", remote: "10.0.0.8:1234", trusted: []string{"10.1.2.3/8"},
			headers: http.Header{"X-Forwarded-For": {"198.51.100.8"}}, ip: "198.51.100.8",
		},
		{
			name: "头里空格与制表符", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"X-Forwarded-For": {" \t198.51.100.8\t ,\t10.0.0.9 "}}, ip: "198.51.100.8",
		},
		{
			name: "可信 HTTPS 和外部域名", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"kakatta.example:8443"}},
			ip:      "10.0.0.8", host: "kakatta.example:8443", secure: true,
		},
		{
			name: "协议忽略大小写和头部空白", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"X-Forwarded-Proto": {" \tHTTPS\t "}}, ip: "10.0.0.8", secure: true,
		},
		{
			name: "可信 HTTP 不覆盖真实 TLS", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"}, tls: true,
			headers: http.Header{"X-Forwarded-Proto": {"http"}}, ip: "10.0.0.8", secure: true,
		},
		{
			name: "标准 Forwarded 头不用", remote: "10.0.0.8:1234", trusted: []string{"10.0.0.0/8"},
			headers: http.Header{"Forwarded": {"for=192.0.2.99;host=attacker.test;proto=https"}}, ip: "10.0.0.8",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://backend.test/path", nil)
			r.RemoteAddr = test.remote
			if test.headers != nil {
				r.Header = test.headers.Clone()
			}
			if test.tls {
				r.TLS = &tls.ConnectionState{}
			}
			originalHeaders := r.Header.Clone()
			peer, err := resolvePeer(r, test.trusted)
			if err != nil {
				t.Fatal(err)
			}
			host := test.host
			if host == "" {
				host = "backend.test"
			}
			if peer.IP != netip.MustParseAddr(test.ip) || peer.Host != host || peer.Secure != test.secure {
				t.Fatalf("来源为 %+v，想要 IP=%s Host=%s Secure=%v", peer, test.ip, host, test.secure)
			}
			if r.Host != "backend.test" || r.RemoteAddr != test.remote || !reflect.DeepEqual(r.Header, originalHeaders) {
				t.Fatal("解析修改了原请求")
			}
		})
	}
}

func TestResolvePeerRejectsInvalidForwardedHeaders(t *testing.T) {
	tests := map[string][]string{
		"X-Forwarded-For": {
			"", " ", "unknown", "198.51.100.", "198.051.100.8", "198.51.100.8:1234", "[2001:db8::8]",
			"\"198.51.100.8\"", "for=198.51.100.8", ",198.51.100.8", "198.51.100.8,", "198.51.100.8,,10.0.0.9",
			"fe80::8%en0", "198.51.100.8\n", "\u00a0198.51.100.8", strings.Repeat("10.0.0.9,", 32) + "10.0.0.9",
			"198.51.100.8" + strings.Repeat(" ", 8193-len("198.51.100.8")),
		},
		"X-Forwarded-Proto": {"", " ", "ftp", "https,http", "http, https", "https\n", "https://"},
		"X-Forwarded-Host": {
			"", " ", "kakatta.test,other.test", "kakatta.test, other.test", "https://kakatta.test", "user@kakatta.test",
			"kakatta.test/", "kakatta.test/path", "kakatta.test?query=1", "kakatta.test#fragment", "kakatta.test\\evil",
			"kakatta.test\n", "kakatta.test\x00", " kakatta.test", "kakatta.test ", "kakatta\ttest", "kakatta\u00a0test",
			":443", "kakatta.test:", "kakatta.test:abc", "kakatta.test:-1", "kakatta.test:65536", "kakatta.test:443:80",
			"[]", "[127.0.0.1]", "[2001:db8::8", "2001:db8::8", "[fe80::8%25en0]", "kakatta%2etest", string([]byte{0xff}),
		},
	}
	for name, values := range tests {
		for _, value := range values {
			t.Run(name+"="+value, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "http://backend.test/path", nil)
				r.RemoteAddr = "10.0.0.8:1234"
				r.Header.Set(name, value)
				peer, err := resolvePeer(r, []string{"10.0.0.0/8"})
				if !errors.Is(err, errRequestPeer) || peer != (requestPeer{}) {
					t.Fatalf("非法头应拒绝，得到 %+v / %v", peer, err)
				}
				if err.Error() != "server.invalid_proxy" {
					t.Fatal("错误带入了请求内容")
				}
			})
		}
	}
}

func TestResolvePeerRejectsMultipleOrMissingHeaderValues(t *testing.T) {
	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host"} {
		for _, values := range [][]string{nil, {}, {"secret-marker", "second-marker"}} {
			t.Run(name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "http://backend.test/path", nil)
				r.RemoteAddr = "10.0.0.8:1234"
				r.Header[name] = values
				_, err := resolvePeer(r, []string{"10.0.0.0/8"})
				if !errors.Is(err, errRequestPeer) || strings.Contains(err.Error(), "marker") {
					t.Fatalf("多值或缺值头应拒绝且错误去敏，得到 %v", err)
				}
			})
		}
	}
}

func TestResolvePeerForwardedForBoundaries(t *testing.T) {
	for _, value := range []string{
		"198.51.100.8" + strings.Repeat(" ", 8192-len("198.51.100.8")),
		"198.51.100.8," + strings.Repeat("10.0.0.9,", 30) + "10.0.0.9",
	} {
		r := httptest.NewRequest(http.MethodGet, "http://backend.test/path", nil)
		r.RemoteAddr = "10.0.0.8:1234"
		r.Header.Set("X-Forwarded-For", value)
		peer, err := resolvePeer(r, []string{"10.0.0.0/8"})
		if err != nil || peer.IP.String() != "198.51.100.8" {
			t.Fatalf("合法边界被拒绝：%+v / %v", peer, err)
		}
	}
}

func TestResolvePeerValidForwardedHosts(t *testing.T) {
	for _, host := range []string{"localhost", "kakatta.example", "KAKATTA.example.", "kakatta.example:0", "kakatta.example:65535", "127.0.0.1:8080", "[2001:db8::8]", "[2001:db8::8]:8443"} {
		t.Run(host, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://backend.test/path", nil)
			r.RemoteAddr = "10.0.0.8:1234"
			r.Header.Set("X-Forwarded-Host", host)
			peer, err := resolvePeer(r, []string{"10.0.0.0/8"})
			if err != nil || peer.Host != host {
				t.Fatalf("合法外部域名应保留：%+v / %v", peer, err)
			}
		})
	}
}

func TestResolvePeerRejectsInvalidConfigurationAndRemote(t *testing.T) {
	for _, trusted := range [][]string{{"secret-marker"}, {"10.0.0.0/33"}, {"fe80::1%en0"}, {" 10.0.0.8"}, {""}} {
		r := httptest.NewRequest(http.MethodGet, "http://backend.test/path", nil)
		if _, err := resolvePeer(r, trusted); !errors.Is(err, errRequestPeer) || strings.Contains(err.Error(), "marker") {
			t.Fatalf("非法可信配置应拒绝且错误去敏：%v", err)
		}
	}
	for _, remote := range []string{"", "secret-marker", "198.51.100.8", "backend.test:1234", "198.51.100.8:http", "198.51.100.8:65536", "2001:db8::8:1234"} {
		r := httptest.NewRequest(http.MethodGet, "http://backend.test/path", nil)
		r.RemoteAddr = remote
		if _, err := resolvePeer(r, nil); !errors.Is(err, errRequestPeer) || strings.Contains(err.Error(), "marker") {
			t.Fatalf("非法连接来源应拒绝且错误去敏：%v", err)
		}
	}
}
