package server

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestJapaneseSettingReachesIdentityAndValidationReasons(t *testing.T) {
	f := newServerFixture(t, true)
	requireStatus(t, f.request(http.MethodPatch, "/api/v1/settings", `{"values":{"general.language":"ja"}}`, true), http.StatusOK)
	identity := f.request(http.MethodGet, "/api/v1/me", "", true)
	requireStatus(t, identity, http.StatusOK)
	if decodeBody[meResponse](t, identity).Language != "ja" {
		t.Fatal("身份接口没有返回持久化的日语设置")
	}
	invalid := f.request(http.MethodPatch, "/api/v1/settings", `{"values":{"general.language":"fr"}}`, true)
	requireStatus(t, invalid, http.StatusUnprocessableEntity)
	problem := decodeBody[ErrorEnvelope](t, invalid)
	if len(problem.Fields) != 1 || problem.Fields[0].Key != "general.language" {
		t.Fatal("未知语言没有按原设置校验返回字段原因", problem)
	}
	reason, err := locales.Format(locales.Ja, problem.Fields[0].Reason)
	if err != nil || reason == "" || !strings.Contains(reason, "選択") {
		t.Fatal("设置拒绝原因没有实际日语文案", reason, err)
	}
	if decodeBody[meResponse](t, f.request(http.MethodGet, "/api/v1/me", "", true)).Language != "ja" {
		t.Fatal("非法语言覆盖了已保存设置")
	}
}

func TestDiagnosticManifestHasJapaneseExplanationsAndKeepsOriginalEvidence(t *testing.T) {
	f, dir := diagnosticFixture(t)
	base := "diagnostics/accounts/1/bundles/bundle-20261112-080000-login/"
	diagnosticWrite(t, dir, base+"page.json", `{"login_page":true}`)
	diagnosticWrite(t, dir, base+"page.html", "<html>卖家原文と元データ</html>")
	diagnosticWrite(t, dir, base+"screenshot.jpg", "not-for-export")
	response := f.request(http.MethodGet, "/api/v1/system/diagnostics", "", true)
	requireStatus(t, response, http.StatusOK)
	manifest := decodeBody[DiagnosticManifest](t, response)
	if len(manifest.Excluded) != 1 || len(manifest.Files) != 2 {
		t.Fatal("语言接入改变了诊断文件筛选", manifest)
	}
	got, err := locales.Format(locales.Ja, manifest.Excluded[0].Reason)
	if err != nil || got != "ログインページのスクリーンショットを除外しました（QRコードの認証情報を含む可能性があります）" {
		t.Fatal("诊断清单原因没有实际日语说明", got, err)
	}
	response = f.request(http.MethodGet, "/api/v1/system/diagnostics/download", "", true)
	requireStatus(t, response, http.StatusOK)
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil || len(archive.File) != 2 {
		t.Fatal("诊断下载改变了已确认的文件集合", err)
	}
	found := false
	for _, entry := range archive.File {
		if strings.HasSuffix(entry.Name, "screenshot.jpg") {
			t.Fatal("诊断下载包含已排除的登录截图")
		}
		if entry.Name == base+"page.html" {
			file, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(file)
			file.Close()
			if err != nil || string(content) != "<html>卖家原文と元データ</html>" {
				t.Fatal("语言接入改写了诊断现场原文", string(content), err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("诊断下载遗漏页面原文")
	}
}
