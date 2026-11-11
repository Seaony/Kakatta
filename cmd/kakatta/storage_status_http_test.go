package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/auth"
)

func TestStorageFailureDiagnosticsStayReadableWithoutWrites(t *testing.T) {
	g := graphFixture(t)
	session := graphAdmin(t, g)
	if err := g.core.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.database.DB().Exec(`CREATE TRIGGER storage_http_clock BEFORE INSERT ON meta
		WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'private-storage-sentinel'); END`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = g.database.DB().Exec(`DROP TRIGGER IF EXISTS storage_http_clock`) })
	if err := g.core.Start(); err != nil {
		t.Fatal(err)
	}
	graphWait(t, func() bool {
		view, err := g.core.GetSystemStatus(t.Context())
		return err != nil || !view.Scheduler.Running
	})
	var sequence int
	var schema, databasePath string
	if err := g.database.DB().QueryRow(`PRAGMA database_list`).Scan(&sequence, &schema, &databasePath); err != nil {
		t.Fatal(err)
	}
	graphWait(t, func() bool {
		files, err := filepath.Glob(filepath.Join(filepath.Dir(databasePath), "diagnostics", "storage", "events-*.jsonl"))
		if err != nil || len(files) != 1 {
			return false
		}
		logged, err := os.ReadFile(files[0])
		return err == nil && strings.Contains(string(logged), "private-storage-sentinel") && strings.Contains(string(logged), "runtime.storage_unavailable")
	})
	if response := graphRequest(t, g, auth.Session{}, http.MethodGet, "/healthz", ""); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("存储保护停止后健康检查仍通过：%d，%s", response.Code, response.Body.String())
	}
	var beforeUsed, beforeExpires int64
	if err := g.database.DB().QueryRow(`SELECT last_used_at,expires_at FROM auth_tokens WHERE id=?`, session.Identity.TokenID).Scan(&beforeUsed, &beforeExpires); err != nil {
		t.Fatal(err)
	}
	if _, err := g.database.DB().Exec(`CREATE TRIGGER storage_http_auth BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT,'private-auth-sentinel'); END`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = g.database.DB().Exec(`DROP TRIGGER IF EXISTS storage_http_auth`) })
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, test := range []struct {
			path   string
			status int
		}{
			{"/api/v1/me", http.StatusOK},
			{"/api/v1/settings", http.StatusOK},
			{"/api/v1/settings/definitions", http.StatusOK},
			{"/api/v1/system/status", http.StatusServiceUnavailable},
		} {
			response := graphRequest(t, g, session, method, test.path, "")
			if response.Code != test.status || response.Header().Get("Set-Cookie") != "" {
				t.Fatalf("故障诊断依赖写库或改了Cookie：%s %s，%d，%s", method, test.path, response.Code, response.Body.String())
			}
			if method == http.MethodGet && test.path == "/api/v1/system/status" && !strings.Contains(response.Body.String(), `"code":"runtime.storage_unavailable"`) {
				t.Fatal("没有返回安全的存储停止原因", response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-") {
				t.Fatal("错误响应暴露了原始存储错误")
			}
		}
	}
	for _, credential := range []auth.Session{{}, {Token: "invalid-token"}} {
		response := graphRequest(t, g, credential, http.MethodGet, "/api/v1/system/status", "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("故障诊断绕过鉴权：%d，%s", response.Code, response.Body.String())
		}
	}
	if response := graphRequest(t, g, session, http.MethodPatch, "/api/v1/settings", `{"values":{"general.language":"en"}}`); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("写操作借故障绕过鉴权写保护：%d，%s", response.Code, response.Body.String())
	}
	if response := graphRequest(t, g, session, http.MethodGet, "/api/v1/security/tokens", ""); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("诊断之外的接口启用了只读鉴权：%d，%s", response.Code, response.Body.String())
	}
	var afterUsed, afterExpires int64
	if err := g.database.DB().QueryRow(`SELECT last_used_at,expires_at FROM auth_tokens WHERE id=?`, session.Identity.TokenID).Scan(&afterUsed, &afterExpires); err != nil || beforeUsed != afterUsed || beforeExpires != afterExpires {
		t.Fatalf("只读诊断修改了会话时间：%v", err)
	}
	for _, statement := range []string{`DROP TRIGGER storage_http_clock`, `DROP TRIGGER storage_http_auth`} {
		if _, err := g.database.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var beforeWrites, afterWrites int64
	if err := g.database.DB().QueryRow(`SELECT total_changes()`).Scan(&beforeWrites); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/settings", `{"values":{"general.language":"en"}}`},
		{http.MethodPost, "/api/v1/auth/login", `{"username":"本地用户","password":"graph-test-password"}`},
	} {
		response := graphRequest(t, g, session, test.method, test.path, test.body)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"runtime.storage_unavailable"`) {
			t.Fatalf("磁盘恢复可写后绕过了尚未重启的闭锁：%s，%d，%s", test.path, response.Code, response.Body.String())
		}
	}
	if err := g.database.DB().QueryRow(`SELECT total_changes()`).Scan(&afterWrites); err != nil || beforeWrites != afterWrites {
		t.Fatalf("清障但尚未重启时仍发生写入：%d→%d，%v", beforeWrites, afterWrites, err)
	}
}

func TestHealthyHTTPReadsDoNotBypassFailedSessionRenewal(t *testing.T) {
	g := graphFixture(t)
	session := graphAdmin(t, g)
	if _, err := g.database.DB().Exec(`CREATE TRIGGER storage_http_auth BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT,'cannot renew'); END`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/me", "/api/v1/settings", "/api/v1/settings/definitions", "/api/v1/system/status"} {
		if response := graphRequest(t, g, session, http.MethodGet, path, ""); response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"auth.storage"`) {
			t.Fatalf("正常运行状态把续期写入失败当成只读放行：%s，%d，%s", path, response.Code, response.Body.String())
		}
	}
}
