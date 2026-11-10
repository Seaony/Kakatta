package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
)

func TestRejectedSlowBodiesHaveBoundedDrain(t *testing.T) {
	for _, name := range []string{"media", "unauthenticated", "cross_origin", "health", "closed", "settings", "forwarded", "static_method"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newServerFixture(t, true)
			path, media, headers, status := "/api/v1/security/tokens", "application/json", "", http.StatusUnauthorized
			switch name {
			case "media":
				media, status = "text/plain", http.StatusUnsupportedMediaType
				headers = "Cookie: " + cookieName + "=" + f.session.Token + "\r\n"
			case "cross_origin":
				headers, status = "Sec-Fetch-Site: cross-site\r\n", http.StatusForbidden
			case "health":
				path, status = "/healthz", http.StatusMethodNotAllowed
			case "closed":
				f.server.Close()
				status = http.StatusServiceUnavailable
			case "settings":
				f.settings.mu.Lock()
				f.settings.err = errors.New("本地测试依赖失败")
				f.settings.mu.Unlock()
				status = http.StatusServiceUnavailable
			case "forwarded":
				if _, err := f.settings.Apply(t.Context(), settings.Values{"security.trusted_proxies": json.RawMessage(`["127.0.0.1"]`)}); err != nil {
					t.Fatal(err)
				}
				headers, status = "X-Forwarded-For: invalid\r\n", http.StatusBadRequest
			case "static_method":
				path, status = "/assets/a.js", http.StatusMethodNotAllowed
			}
			closed := make(chan struct{}, 1)
			server := httptest.NewUnstartedServer(f.server)
			server.Config.ReadHeaderTimeout = 5 * time.Second
			server.Config.IdleTimeout = time.Minute
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					select {
					case closed <- struct{}{}:
					default:
					}
				}
			}
			server.Start()
			defer server.Close()
			connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(13 * time.Second)); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if _, err := fmt.Fprintf(connection, "POST %s HTTP/1.1\r\nHost: kakatta.test\r\nContent-Type: %s\r\nContent-Length: 100\r\n%s\r\nx", path, media, headers); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatalf("拒绝路径仍被未读请求体拖住：%v", err)
			}
			defer response.Body.Close()
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != status || time.Since(start) > 12*time.Second {
				t.Fatalf("拒绝请求没有在期限内完成：status=%d，预期%d", response.StatusCode, status)
			}
			f.server.limiter.mu.Lock()
			active := f.server.limiter.inflight
			f.server.limiter.mu.Unlock()
			if active != 0 {
				t.Fatal("拒绝请求结束后仍占用登录名额")
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("超时请求的连接未及时回收")
			}
		})
	}
}
