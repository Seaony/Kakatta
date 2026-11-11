package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWritableUsesTemporaryFileAndPreservesExistingFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(private, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	check := writable("data_directory", dir)
	if !check.Passed || check.Action != "" {
		t.Fatalf("可写目录未通过：%+v", check)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "secret.key" {
		t.Fatalf("自检残留文件或移除了原文件：%v，%v", entries, err)
	}
	content, err := os.ReadFile(private)
	if err != nil || string(content) != "original" {
		t.Fatalf("自检改动了原有文件：%q，%v", content, err)
	}
}

func TestWritableFailureGivesRemedy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"", filepath.Join(file, "child")} {
		check := writable("log_directory", dir)
		if check.Passed || check.Action == "" || check.Detail == "" {
			t.Fatalf("目录失败没有明确原因和处理办法：%+v", check)
		}
	}
}

func TestTimezoneUsesExplicitLocation(t *testing.T) {
	for _, name := range []string{"", "Local", "not/a_timezone"} {
		if check := timezone(name); check.Passed || check.Action == "" {
			t.Fatalf("非法时区被接受：%q，%+v", name, check)
		}
	}
	check := timezone("Asia/Shanghai")
	if !check.Passed || check.Values["timezone"] != "Asia/Shanghai" {
		t.Fatalf("明确时区未通过：%+v", check)
	}
	local, err := time.Parse(time.RFC3339, check.Values["local_time"].(string))
	_, offset := local.Zone()
	if err != nil || offset != 8*60*60 {
		t.Fatalf("检查结果没有使用配置时区：%v，%v", local, err)
	}
}

func TestDiskResultIncludesSpaceAndFailureRemedy(t *testing.T) {
	check := disk(t.TempDir())
	if !check.Passed || check.Values["available_bytes"].(uint64) == 0 {
		t.Fatalf("没有读取真实数据目录的剩余空间：%+v", check)
	}
	for _, check := range []Check{diskResult(0, nil), diskResult(0, errors.New("local disk unavailable")), disk(filepath.Join(t.TempDir(), "missing"))} {
		if check.Passed || check.Detail == "" || check.Action == "" {
			t.Fatalf("磁盘失败没有明确说明：%+v", check)
		}
	}
}

func TestProxyOnlyConnectsEndpointWithoutSendingData(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			type received struct {
				body []byte
				err  error
			}
			done := make(chan received, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- received{err: err}
					return
				}
				defer conn.Close()
				conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				data, err := io.ReadAll(conn)
				done <- received{data, err}
			}()
			upstream := &url.URL{Scheme: scheme, Host: listener.Addr().String(), User: url.UserPassword("private-user", "private-password"), Path: "/private-path", RawQuery: "token=private-token"}
			check := proxy(t.Context(), upstream)
			if !check.Passed {
				t.Fatalf("本地代理端口未通过：%+v", check)
			}
			select {
			case got := <-done:
				if got.err != nil || len(got.body) != 0 {
					t.Fatalf("自检向代理发送了数据：%q，%v", got.body, got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("代理连接没有及时关闭")
			}
			encoded, err := json.Marshal(check)
			if err != nil || strings.Contains(string(encoded), "private-") {
				t.Fatalf("自检结果泄露代理凭据或参数：%s，%v", encoded, err)
			}
		})
	}
}

func TestProxyFailureAndNoConfiguredProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")
	if check := proxy(t.Context(), nil); !check.Passed || check.Values != nil {
		t.Fatalf("无账号代理时使用了环境代理：%+v", check)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, upstream := range []*url.URL{
		{Scheme: "https", Host: "127.0.0.1:1"},
		{Scheme: "http"},
		{Scheme: "socks5", Host: "127.0.0.1:1"},
	} {
		check := proxy(ctx, upstream)
		if check.Passed || check.Action == "" || check.Detail == "" {
			t.Fatalf("代理失败没有明确说明：%+v", check)
		}
	}
}

func TestAllConfiguredProxiesMustPassAndEveryEndpointIsChecked(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		body, err := io.ReadAll(conn)
		if len(body) != 0 {
			err = errors.New("收到请求或凭据")
		}
		done <- err
	}()
	check := proxies(t.Context(), []*url.URL{
		{Scheme: "unsupported", Host: "127.0.0.1:1", User: url.UserPassword("private-user", "private-password")},
		{Scheme: "http", Host: listener.Addr().String(), User: url.UserPassword("private-user", "private-password")},
	})
	if check.Passed || check.Values["count"] != 2 || check.Action == "" {
		t.Fatalf("部分代理失败却被放行：%+v", check)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("第一个代理失败后没有检查后续代理")
	}
	encoded, err := json.Marshal(check)
	if err != nil || strings.Contains(string(encoded), "private-") {
		t.Fatalf("多个代理结果泄露了凭据：%s，%v", encoded, err)
	}
	for _, check := range []Check{proxies(t.Context(), nil), proxies(t.Context(), []*url.URL{nil})} {
		count := check.Values["count"].(int)
		if check.Passed != (count == 0) {
			t.Fatalf("未配置和缺失配置没有区分：%+v", check)
		}
	}
}

func TestInvalidSetupDoesNotLaunchBrowser(t *testing.T) {
	dir := t.TempDir()
	checks := Run(t.Context(), Options{DataDir: dir, Timezone: "not/a_timezone"})
	if len(checks) != 9 {
		t.Fatalf("没有逐项返回检查结果：%+v", checks)
	}
	for _, check := range checks {
		switch check.ID {
		case "timezone", "chrome", "display", "remote", "cleanup":
			if check.Passed || check.Action == "" {
				t.Fatalf("前置失败后仍放行浏览器：%+v", check)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "logs" {
		t.Fatalf("无效配置仍创建了浏览器目录：%v，%v", entries, err)
	}
}
