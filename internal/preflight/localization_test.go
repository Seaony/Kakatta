package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestRunUsesSelectedLanguageAndKeepsDefaultChinese(t *testing.T) {
	dir := t.TempDir()
	base := Options{DataDir: dir, Timezone: "not/a_timezone"}
	defaults := Run(t.Context(), base)
	if defaults[0].Detail != "目录可创建、写入并同步文件" || defaults[3].Detail != "时区不存在" {
		t.Fatalf("省略语言改变了原中文输出：%+v", defaults)
	}
	for _, language := range []locales.Language{locales.En, locales.Ja} {
		t.Run(string(language), func(t *testing.T) {
			o := base
			o.Language = language
			checks := Run(t.Context(), o)
			if len(checks) != len(defaults) {
				t.Fatalf("语言改变了检查项：%+v", checks)
			}
			for i, check := range checks {
				if check.ID != defaults[i].ID || check.Passed != defaults[i].Passed {
					t.Errorf("语言改变了检查结构：%+v，%+v", check, defaults[i])
				}
				if check.Detail == "" || strings.HasPrefix(check.Detail, "preflight.") {
					t.Errorf("说明没有实际译文：%+v", check)
				}
				if (check.Action == "") != (defaults[i].Action == "") {
					t.Errorf("处理建议没有实际译文：%+v", check)
				}
				var object map[string]any
				encoded, err := json.Marshal(check)
				if err != nil || json.Unmarshal(encoded, &object) != nil {
					t.Fatalf("检查结果不能编码：%+v，%v", check, err)
				}
				for key := range object {
					if key != "id" && key != "passed" && key != "detail" && key != "action" && key != "values" {
						t.Errorf("语言改变了结果字段：%s", key)
					}
				}
			}
			if language == locales.En && checks[3].Detail != "The timezone does not exist" {
				t.Errorf("没有实际英文输出：%+v", checks[3])
			}
			if language == locales.Ja && !strings.Contains(checks[3].Detail, "タイムゾーン") {
				t.Errorf("没有实际日文输出：%+v", checks[3])
			}
		})
	}
}

func TestLocalizedChecksKeepStateAndRawErrorArguments(t *testing.T) {
	raw := "upstream {error} {available} {count}"
	file := filepath.Join(t.TempDir(), "not-a-directory-{error}")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, language := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		t.Run(string(language), func(t *testing.T) {
			failed := diskResult(0, errors.New(raw), language)
			if failed.Passed || failed.Values != nil || failed.Action == "" || !strings.HasSuffix(failed.Detail, raw) || strings.Count(failed.Detail, raw) != 1 {
				t.Fatalf("原始错误被改写或错误状态丢失：%+v", failed)
			}
			written := writable("data_directory", filepath.Join(file, "child"), language)
			if written.Passed || written.Action == "" || !strings.Contains(written.Detail, "not-a-directory-{error}") {
				t.Fatalf("系统错误中的参数被二次替换：%+v", written)
			}
			invalid := &url.URL{Scheme: "https", Host: "127.0.0.1:1"}
			checks := []Check{
				writable("data_directory", "", language), timezone("Local", language),
				proxy(ctx, invalid, language), diskResult(0, nil, language),
			}
			for _, check := range checks {
				if check.Passed || check.Action == "" || check.Detail == "" {
					t.Errorf("翻译丢失检查结果或处理办法：%+v", check)
				}
			}
			multiple := proxies(ctx, []*url.URL{nil, invalid}, language)
			if multiple.Passed || multiple.Detail == "" || multiple.Action == "" || !reflect.DeepEqual(multiple.Values, map[string]any{"count": 2, "addresses": []string{}}) {
				t.Fatalf("翻译改变了多个代理检查结果：%+v", multiple)
			}
			available := diskResult(1536*1024, nil, language)
			if !available.Passed || available.Action != "" || available.Values["available_bytes"] != uint64(1536*1024) || !strings.Contains(available.Detail, "1.5 MiB") {
				t.Fatalf("翻译改变了磁盘检查数值：%+v", available)
			}
		})
	}
}

func TestPreflightCatalogCoversFixedCopy(t *testing.T) {
	codes := []string{
		"setup_skipped", "setup_remedy", "proxy_missing", "proxies_failed", "proxies_remedy", "proxy_unconfigured", "proxies_connected", "failure_separator",
		"directory_remedy", "directory_unspecified", "directory_failed", "directory_writable", "timezone_remedy", "timezone_unspecified", "timezone_unknown", "timezone_valid",
		"proxy_remedy", "proxy_invalid", "proxy_connect_failed", "proxy_close_failed", "proxy_connected", "disk_remedy", "disk_read_failed", "disk_full", "disk_available",
		"profile_failed", "mount_remedy", "processes_not_started", "chrome_remaining", "cleanup_done", "cleanup_failed", "cleanup_remedy", "logs_open_failed", "logs_remedy", "logs_write_failed", "port_remedy",
		"display_failed", "display_remedy", "display_not_ready", "display_first_remedy", "display_started", "chrome_remedy", "chrome_start_failed", "chrome_url_failed", "chrome_not_blank", "chrome_version_failed", "chrome_started",
		"remote_remedy", "remote_failed", "remote_started", "browser_logs_failed", "browser_logs_remedy", "port_unavailable", "linux_required", "container_remedy",
	}
	args := map[string]any{"error": "upstream {error}", "count": 2, "failed": 1, "errors": "upstream {errors}", "available": "1.5"}
	for _, language := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		t.Run(string(language), func(t *testing.T) {
			for _, code := range codes {
				got, err := locales.Format(language, locales.Message{Code: "preflight." + code, Args: args})
				if err != nil || got == "" {
					t.Errorf("固定自检说明未翻译：%s，%q，%v", code, got, err)
				}
			}
		})
	}
}
