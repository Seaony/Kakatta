package server

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/locales"
)

func diagnosticFixture(t *testing.T) (*serverFixture, string) {
	t.Helper()
	f := newServerFixture(t, true)
	dir := t.TempDir()
	if err := f.server.RegisterDiagnostics(dir); err != nil {
		t.Fatal(err)
	}
	return f, dir
}

func diagnosticWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	filename := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsPreviewAndDownloadOnlyKnownLocalArtifacts(t *testing.T) {
	f, dir := diagnosticFixture(t)
	wanted := map[string]string{
		"diagnostics/service/events-2026-11-12.jsonl":                                "{\"event\":\"环境\",\"Chrome\":\"157\"}\n",
		"diagnostics/accounts/1/chrome.log":                                          "Chrome: local crash\n",
		"diagnostics/accounts/1/xvfb.log.1":                                          "Xvfb: local display\n",
		"diagnostics/accounts/1/x11vnc.log":                                          "VNC: local startup\n",
		"diagnostics/accounts/1/bundles/bundle-20261112-080000-local/page.json":      "{\"url\":\"https://www.goofish.com/item?id=123\",\"login_page\":false}",
		"diagnostics/accounts/1/bundles/bundle-20261112-080000-local/events.json":    "[]",
		"diagnostics/accounts/1/bundles/bundle-20261112-080000-local/screenshot.jpg": "local screenshot bytes",
		"diagnostics/accounts/1/bundles/bundle-20261112-080000-local/page.html":      "<html><body>虚构商品</body></html>",
		"diagnostics/accounts/1/bundles/bundle-20261112-080000-local/response.json":  "{\"data\":\"local parse failure\"}",
		"experiment/2/logs/summaries/summary-20261112-080000-local/summary.json":     "{\"steps\":[]}",
		"experiment/2/logs/events-2026-11-12.jsonl":                                  "{\"event\":\"开始\"}\n",
		"experiment/2/logs/bundles/bundle-20261112-080000-local/page.html":           "<html></html>",
	}
	var total int64
	for name, content := range wanted {
		diagnosticWrite(t, dir, name, content)
		total += int64(len(content))
	}
	for _, name := range []string{
		"secret.key", "kakatta.db", "browser/1/Default/Cookies", "browser/1/events-2026-11-12.jsonl",
		"experiment/2/visits.json", "experiment/2/browser/Default/Cookies", "experiment-fixture/2/logs/chrome.log",
		"diagnostics/secret.key", "diagnostics/accounts/1/secret.key", "diagnostics/accounts/1/sequence.json",
		"diagnostics/accounts/1/bundles/bundle-20261112-080000-local/secret.key",
		"diagnostics/accounts/1/bundles/not-a-program-bundle/response.json", "diagnostics/accounts/1/other/chrome.log",
	} {
		diagnosticWrite(t, dir, name, "outside-private-sentinel")
	}
	preview := f.request(http.MethodGet, "/api/v1/system/diagnostics", "", true)
	requireStatus(t, preview, http.StatusOK)
	manifest := decodeBody[DiagnosticManifest](t, preview)
	if len(manifest.Files) != len(wanted) || manifest.TotalBytes != total {
		t.Fatalf("诊断清单不完整：%+v", manifest)
	}
	for _, entry := range manifest.Files {
		if content, exists := wanted[entry.Path]; !exists || entry.Bytes != int64(len(content)) {
			t.Fatalf("诊断清单包含非白名单或错误大小：%+v", entry)
		}
	}
	response := f.request(http.MethodGet, "/api/v1/system/diagnostics/download", "", true)
	requireStatus(t, response, http.StatusOK)
	if response.Header().Get("Content-Type") != "application/zip" || !strings.Contains(response.Header().Get("Content-Disposition"), "kakatta-diagnostics.zip") {
		t.Fatal("没有提供可下载的诊断压缩包")
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil || len(archive.File) != len(wanted) {
		t.Fatalf("诊断压缩包不完整：%v", err)
	}
	for _, entry := range archive.File {
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(content) != wanted[entry.Name] || strings.Contains(string(content), "outside-private-sentinel") {
			t.Fatalf("诊断文件不符或泄露私有文件：%s，%v", entry.Name, err)
		}
	}
}

func TestDiagnosticsRequiresAuthenticationAndRejectsUserPaths(t *testing.T) {
	f, dir := diagnosticFixture(t)
	diagnosticWrite(t, dir, "diagnostics/service/events-2026-11-12.jsonl", "{}\n")
	for _, route := range []string{"/api/v1/system/diagnostics", "/api/v1/system/diagnostics/download"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			requireStatus(t, f.request(method, route, "", false), http.StatusUnauthorized)
			requireStatus(t, f.request(method, route+"?path=../../secret.key", "", true), http.StatusBadRequest)
			requireStatus(t, f.request(method, route, "{}", true), http.StatusBadRequest)
			requireStatus(t, f.request(method, route, "", true), http.StatusOK)
		}
		requireStatus(t, f.request(http.MethodPost, route, "", true, crossSiteTransferRequest), http.StatusForbidden)
	}
	if f.server.RegisterDiagnostics("") == nil || f.server.RegisterDiagnostics(dir) == nil {
		t.Fatal("空目录或重复注册被接受")
	}
}

func TestDiagnosticsSkipsSymlinksAndStopsBeforeSendingOversizeData(t *testing.T) {
	f, dir := diagnosticFixture(t)
	diagnosticWrite(t, dir, "diagnostics/service/xvfb.log", "safe\n")
	secret := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(secret, []byte("private-link-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "diagnostics", "service", "chrome.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(dir, "diagnostics", "accounts")); err != nil {
		t.Fatal(err)
	}
	manifest := decodeBody[DiagnosticManifest](t, f.request(http.MethodGet, "/api/v1/system/diagnostics", "", true))
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "diagnostics/service/xvfb.log" {
		t.Fatalf("符号链接进入了诊断清单：%+v", manifest)
	}
	file, err := os.OpenFile(filepath.Join(dir, "diagnostics", "service", "x11vnc.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(diagnosticMaxFile + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	for _, route := range []string{"/api/v1/system/diagnostics", "/api/v1/system/diagnostics/download"} {
		response := f.request(http.MethodGet, route, "", true)
		requireStatus(t, response, http.StatusServiceUnavailable)
		if errorCode(response) != "diagnostics.too_large" || strings.Contains(response.Body.String(), dir) || response.Header().Get("Content-Type") == "application/zip" {
			t.Fatal("大包没有在发送前明确拒绝，或错误泄露本机路径")
		}
	}
}

func TestDiagnosticsUnavailableStorageDoesNotExposePaths(t *testing.T) {
	f, dir := diagnosticFixture(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/v1/system/diagnostics", "/api/v1/system/diagnostics/download"} {
		response := f.request(http.MethodGet, route, "", true)
		requireStatus(t, response, http.StatusServiceUnavailable)
		if errorCode(response) != "diagnostics.unavailable" || strings.Contains(response.Body.String(), dir) {
			t.Fatal("无法读取诊断时没有给出分类原因，或泄露了本机路径")
		}
	}
}

func TestDiagnosticsStorageStopUsesOnlyReadOnlyAuthentication(t *testing.T) {
	f, dir := diagnosticFixture(t)
	diagnosticWrite(t, dir, "diagnostics/service/events-2026-11-12.jsonl", "{}\n")
	f.server.runtimeFailure = func() *locales.Message {
		return &locales.Message{Code: "runtime.storage_full", Args: map[string]any{}}
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER diagnostics_auth_guard BEFORE UPDATE ON auth_tokens BEGIN SELECT RAISE(ABORT,'private-readonly-sentinel'); END`); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, route := range []string{"/api/v1/system/diagnostics", "/api/v1/system/diagnostics/download"} {
			response := f.request(method, route, "", true)
			requireStatus(t, response, http.StatusOK)
			if response.Header().Get("Set-Cookie") != "" {
				t.Fatal("只读诊断导出仍续期Cookie")
			}
			requireStatus(t, f.request(method, route, "", false), http.StatusUnauthorized)
			requireStatus(t, f.request(method, route, "", false, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+f.session.Token) }), http.StatusUnauthorized)
		}
	}
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatalf("只读诊断导出写入了数据库：%d→%d，%v", before, after, err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `DROP TRIGGER diagnostics_auth_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE auth_tokens SET revoked_at=1 WHERE id=?`, f.session.Identity.TokenID); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/v1/system/diagnostics", "/api/v1/system/diagnostics/download"} {
		requireStatus(t, f.request(http.MethodGet, route, "", true), http.StatusUnauthorized)
	}
}

func TestDiagnosticsRecordedCredentialsDoNotEnterArchive(t *testing.T) {
	f, dir := diagnosticFixture(t)
	recorder, err := diagnostics.Open(filepath.Join(dir, "diagnostics", "service"), "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Event("环境", map[string]any{
		"password": "private-password-sentinel", "token": "private-token-sentinel", "Cookie": "private-cookie-sentinel",
		"api_key": "private-key-sentinel", "proxy": "http://local-user:private-proxy-sentinel@127.0.0.1:12345",
		"goods": "相机成色很好，盒子齐全",
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	response := f.request(http.MethodGet, "/api/v1/system/diagnostics/download", "", true)
	requireStatus(t, response, http.StatusOK)
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil || len(archive.File) == 0 {
		t.Fatalf("没有导出已落盘的事件：%v", err)
	}
	var combined strings.Builder
	for _, entry := range archive.File {
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(&combined, file)
		file.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
	}
	if strings.Contains(combined.String(), "private-") || !strings.Contains(combined.String(), "相机成色很好，盒子齐全") {
		t.Fatal("导出的实际事件泄露凭据或丢掉商品正文")
	}
}

func TestDiagnosticsExcludesLoginAndUnverifiedScreenshotsButKeepsOtherEvidence(t *testing.T) {
	f, dir := diagnosticFixture(t)
	for _, item := range []struct {
		name, metadata string
	}{{"login", `{"login_page":true}`}, {"regular", `{"login_page":false}`}, {"missing", ""}, {"broken", "{"}, {"legacy", `{"url":"about:blank"}`}} {
		base := "diagnostics/accounts/1/bundles/bundle-20261112-080000-" + item.name
		if item.metadata != "" {
			diagnosticWrite(t, dir, base+"/page.json", item.metadata)
		}
		diagnosticWrite(t, dir, base+"/page.html", "<html>安全的页面资料</html>")
		diagnosticWrite(t, dir, base+"/screenshot.jpg", item.name+"-screenshot-sentinel")
	}
	diagnosticWrite(t, dir, "diagnostics/preflight/events-2026-11-12.jsonl", "{\"event\":\"自检\"}\n")
	manifest := decodeBody[DiagnosticManifest](t, f.request(http.MethodGet, "/api/v1/system/diagnostics", "", true))
	if len(manifest.Excluded) != 4 {
		t.Fatalf("没有列出所有排除的截图：%+v", manifest.Excluded)
	}
	for _, excluded := range manifest.Excluded {
		want := "diagnostics.screenshot_unverified"
		if strings.Contains(excluded.Path, "-login/") {
			want = "diagnostics.login_screenshot_excluded"
		}
		if excluded.Reason.Code != want {
			t.Fatalf("截图排除原因不准确：%+v", excluded)
		}
	}
	response := f.request(http.MethodGet, "/api/v1/system/diagnostics/download", "", true)
	requireStatus(t, response, http.StatusOK)
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var screenshots, pages, preflight int
	for _, entry := range archive.File {
		if strings.HasSuffix(entry.Name, "/screenshot.jpg") {
			screenshots++
			if !strings.Contains(entry.Name, "-regular/") {
				t.Fatalf("登录或无法确认的截图被导出：%s", entry.Name)
			}
		}
		if strings.HasSuffix(entry.Name, "/page.html") {
			pages++
		}
		if strings.HasPrefix(entry.Name, "diagnostics/preflight/") {
			preflight++
		}
	}
	if screenshots != 1 || pages != 5 || preflight != 1 {
		t.Fatalf("排除截图时丢掉其他证据：截图%d、页面%d、自检%d", screenshots, pages, preflight)
	}
	if _, err := os.Stat(filepath.Join(dir, "diagnostics", "accounts", "1", "bundles", "bundle-20261112-080000-login", "screenshot.jpg")); err != nil {
		t.Fatal("导出改变了本机保存的登录截图")
	}
}
