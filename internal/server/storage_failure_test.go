package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestStorageFailureRejectsEveryDocumentedWriteBeforeDispatch(t *testing.T) {
	f := newBusinessFixture(t)
	f.registerTransferContracts(t)
	f.server.runtimeFailure = func() *locales.Message {
		return &locales.Message{Code: "runtime.storage_locked", Args: map[string]any{}}
	}
	for _, guarded := range []bool{false, true} {
		if guarded {
			for _, statement := range []string{
				`CREATE TRIGGER storage_guard_auth_insert BEFORE INSERT ON auth_tokens BEGIN SELECT RAISE(ABORT,'private-write-sentinel'); END`,
				`CREATE TRIGGER storage_guard_auth_update BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT,'private-write-sentinel'); END`,
				`CREATE TRIGGER storage_guard_meta_insert BEFORE INSERT ON meta BEGIN SELECT RAISE(ABORT,'private-write-sentinel'); END`,
				`CREATE TRIGGER storage_guard_meta_update BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'private-write-sentinel'); END`,
			} {
				if _, err := f.store.DB().ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
		}
		var before, after int64
		if err := f.store.DB().QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, operation := range testAPIOperations(t) {
			switch operation.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				continue
			}
			_, contract, err := operationHandler(f.server.mux, operation)
			if err != nil || contract {
				t.Fatalf("写接口缺少真实处理器：%s %s，%v", operation.Method, operation.Path, err)
			}
			body := `{}`
			if operation.Path == "/api/v1/auth/login" {
				body = `{"username":"本地用户","password":"` + testPassword + `"}`
			}
			for _, authenticated := range []bool{false, true} {
				response := f.request(operation.Method, sampleRoutePath(operation.Path), body, authenticated)
				envelope := decodeBody[ErrorEnvelope](t, response)
				if response.Code != http.StatusServiceUnavailable || envelope.Error.Code != "runtime.storage_locked" || response.Header().Get("Set-Cookie") != "" || strings.Contains(response.Body.String(), "private-") {
					t.Fatalf("故障停机仍执行写接口：%s %s，数据库拒写=%t，有凭据=%t，%d，%s", operation.Method, operation.Path, guarded, authenticated, response.Code, response.Body.String())
				}
			}
			count++
		}
		if count == 0 {
			t.Fatal("没有验证任何写接口")
		}
		if err := f.store.DB().QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&after); err != nil || before != after {
			t.Fatalf("诊断停机期间仍发生数据库写入：%d→%d，%v", before, after, err)
		}
		t.Logf("已验证%d条实际写路由，数据库拒写=%t，匿名和已登录均在分发前拒绝", count, guarded)
	}
	response := f.request(http.MethodPost, "/api/v1/auth/login", `{}`, false, func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	requireStatus(t, response, http.StatusForbidden)
}

func TestStorageFailureOnlyAllowsDiagnosticReadAuthentication(t *testing.T) {
	f := newServerFixture(t, true)
	f.server.runtimeFailure = func() *locales.Message {
		return &locales.Message{Code: "runtime.storage_unavailable", Args: map[string]any{}}
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER storage_auth_guard BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT,'no renewal'); END`); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"/api/v1/me", "/api/v1/settings", "/api/v1/settings/definitions"} {
			response := f.request(method, path, "", true)
			requireStatus(t, response, http.StatusOK)
			if response.Header().Get("Set-Cookie") != "" {
				t.Fatal("只读鉴权仍重发Cookie")
			}
			requireStatus(t, f.request(method, path, "", false), http.StatusUnauthorized)
			requireStatus(t, f.request(method, path, "", false, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+f.session.Token)
			}), http.StatusUnauthorized)
		}
	}
	requireStatus(t, f.request(http.MethodGet, "/api/v1/security/tokens", "", true), http.StatusServiceUnavailable)
	if _, err := f.store.DB().ExecContext(t.Context(), `DROP TRIGGER storage_auth_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE auth_tokens SET revoked_at=1 WHERE id=?`, f.session.Identity.TokenID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(http.MethodGet, "/api/v1/me", "", true), http.StatusUnauthorized)
}
