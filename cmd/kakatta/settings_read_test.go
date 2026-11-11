package main

import (
	"net/http"
	"testing"

	"github.com/Seaony/Kakatta/internal/auth"
)

func TestHTTPReadsNeverSaveQuotaClock(t *testing.T) {
	g := graphFixture(t)
	session := graphAdmin(t, g)
	var before string
	if err := g.database.DB().QueryRow(`SELECT value FROM meta WHERE key='quota.clock'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := g.database.DB().Exec(`CREATE TRIGGER forbid_clock_write BEFORE INSERT ON meta
		WHEN NEW.key IN ('quota.clock','quota.clock_initialized') BEGIN SELECT RAISE(FAIL,'read only'); END`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/settings", "/api/v1/tasks", "/api/v1/accounts", "/api/v1/system/status", "/"} {
		response := graphRequest(t, g, session, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("读取依赖时钟可写：%s，%d，%s", path, response.Code, response.Body.String())
		}
	}
	response := graphRequest(t, g, auth.Session{}, http.MethodGet, "/api/v1/tasks", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("匿名请求在鉴权前尝试写时钟：%d", response.Code)
	}
	var after string
	if err := g.database.DB().QueryRow(`SELECT value FROM meta WHERE key='quota.clock'`).Scan(&after); err != nil || before != after {
		t.Fatalf("读取修改了额度时钟：%v", err)
	}
}
