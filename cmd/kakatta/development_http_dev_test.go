//go:build kakatta_dev

package main

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/backup"
)

func TestDevelopmentControlAcceptsManagerRandomText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-token")
	want := rand.Text()
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readDevelopmentControl(path); err != nil || got != want {
		t.Fatal("管理进程生成的随机凭据被后端拒绝", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDevelopmentControl(path); err == nil {
		t.Fatal("非私有文件被接受")
	}
}

func developmentRequest(handler http.Handler, method, path, body, key, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:18080"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:43210"
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("X-Kakatta-Development-Control", key)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestDevelopmentAutoLoginKeepsOriginProtectionAndRejectsRebinding(t *testing.T) {
	m, _ := runtimeFixture(t, backup.KeyDefaultFile)
	handler := newDevelopmentHandler(m, true, strings.Repeat("k", 32))
	if response := developmentRequest(handler, "GET", "/api/v1/me", "", "", ""); response.Code != http.StatusOK {
		t.Fatal("本机假数据仍要求登录", response.Code, response.Body.String())
	}
	if response := developmentRequest(handler, "PATCH", "/api/v1/settings", `{"values":{"general.timezone":"UTC"}}`, "", "https://other.example.invalid"); response.Code != http.StatusForbidden {
		t.Fatal("自动登录绕过了写操作来源检查", response.Code)
	}
	request := httptest.NewRequest("GET", "http://other.example.invalid/api/v1/me", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("开发入口接受了非回环Host")
	}
	request = httptest.NewRequest("GET", "http://127.0.0.1:18080/api/v1/me", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("开发入口接受了非回环连接")
	}
}

func TestDevelopmentRealModeNeverCreatesAnonymousSession(t *testing.T) {
	m, _ := runtimeFixture(t, backup.KeyDefaultFile)
	handler := newDevelopmentHandler(m, false, strings.Repeat("k", 32))
	if response := developmentRequest(handler, "GET", "/api/v1/me", "", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatal("真实容器开发绕过了正式认证", response.Code)
	}
	if response := developmentRequest(handler, "GET", "/__dev/backend/status", "", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatal("管理接口缺少独立私有凭据检查", response.Code)
	}
	if response := developmentRequest(handler, "POST", "/__dev/backend/scheduler", `{"paused":true}`, "wrong", ""); response.Code != http.StatusUnauthorized || m.graph.core.DevelopmentSchedulerPaused() {
		t.Fatal("无凭据请求改变了调度")
	}
}

func TestDevelopmentReadyFreezesAdmissionAndStillAllowsFormalShutdown(t *testing.T) {
	m, _ := runtimeFixture(t, backup.KeyDefaultFile)
	key := strings.Repeat("k", 32)
	handler := newDevelopmentHandler(m, false, key)
	if response := developmentRequest(handler, "POST", "/__dev/backend/scheduler", `{"paused":true}`, key, ""); response.Code != http.StatusOK || !m.graph.core.DevelopmentSchedulerPaused() {
		t.Fatal("未能暂停调度", response.Code)
	}
	if response := developmentRequest(handler, "POST", "/__dev/backend/scheduler", `{"paused":false}`, key, ""); response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	if response := developmentRequest(handler, "POST", "/__dev/backend/prepare-reload", "", key, ""); response.Code != http.StatusOK {
		t.Fatal("无法排空", response.Code, response.Body.String())
	}
	if response := developmentRequest(handler, "GET", "/healthz", "", "", ""); response.Code != http.StatusServiceUnavailable {
		t.Fatal("切换期间网页仍可接纳业务请求", response.Code)
	}
	if response := developmentRequest(handler, "POST", "/__dev/backend/scheduler", `{"paused":false}`, key, ""); response.Code != http.StatusConflict {
		t.Fatal("切换期间还能放开调度", response.Code)
	}
	if response := developmentRequest(handler, "POST", "/__dev/backend/cancel-reload", "", key, ""); response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	if response := developmentRequest(handler, "GET", "/healthz", "", "", ""); response.Code != http.StatusOK || m.graph.core.DevelopmentSchedulerPaused() {
		t.Fatal("取消切换没有恢复原服务", response.Code)
	}
	if response := developmentRequest(handler, "POST", "/__dev/backend/prepare-reload", "", key, ""); response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal("正式停机被开发屏障卡住", err)
	}
}
