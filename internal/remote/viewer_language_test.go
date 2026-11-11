package remote

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestViewerDocumentUsesRequestLanguageAndAllCopies(t *testing.T) {
	s := testSession(t, "127.0.0.1:1")
	for _, tc := range []struct{ header, language string }{
		{"zh-CN", "zh-CN"}, {"en-US", "en"}, {"en;q=0.1,ja-JP;q=0.9", "ja"}, {"fr-FR", "en"},
	} {
		t.Run(tc.header, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Accept-Language", tc.header)
			response := httptest.NewRecorder()
			s.Handler(func(*http.Request) bool { return true }).ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `<html lang="`+tc.language+`">`) {
				t.Fatal("正式画面没有跟随请求语言", response.Code, body)
			}
			for _, language := range []string{"zh-CN", "en", "ja"} {
				if strings.Count(body, `<span lang="`+language+`"`) != 1 {
					t.Fatal("正式画面缺少切换语言所需的副本", language)
				}
			}
			if strings.Contains(body, "回到实验工具") || strings.Contains(body, "{{") {
				t.Fatal("正式画面误用实验流程或未完成模板替换")
			}
			spans := regexp.MustCompile(`<span lang="([^"]+)" ([^>]+)></span>`).FindAllStringSubmatch(body, -1)
			if len(spans) != 3 {
				t.Fatal("三份副本没有完整输出", spans)
			}
			for _, span := range spans {
				attributes := regexp.MustCompile(`data-([a-z-]+)="([^"]*)"`).FindAllStringSubmatch(span[2], -1)
				if len(attributes) != 27 {
					t.Fatal("正式画面有遗漏的控件或状态文案", span[1], len(attributes))
				}
				for _, attribute := range attributes {
					code := "remote.viewer." + strings.ReplaceAll(attribute[1], "-", "_")
					want, err := locales.Format(locales.Language(span[1]), locales.Message{Code: code})
					if err != nil || want == "" || html.UnescapeString(attribute[2]) != want {
						t.Fatal("副本文案没有完整读取共享字典", code, span[1], attribute[2], err)
					}
				}
			}
			if tc.language == "ja" && !strings.Contains(body, `<strong id="viewer-heading">ブラウザのリモート画面</strong>`) {
				t.Fatal("正式画面标题没有实际日文译文")
			}
		})
	}
}

func TestViewerHTTPFailuresKeepAuthenticationOriginAndSafeJapaneseReasons(t *testing.T) {
	s := testSession(t, "127.0.0.1:1")
	for _, tc := range []struct {
		path, origin, text string
		authorized         bool
		status             int
	}{
		{"/", "", "先にログインしてください。", false, http.StatusUnauthorized},
		{"/vnc", "http://other.test", "同じサイトからの接続のみ", true, http.StatusForbidden},
		{"/vnc", "http://tool.test", "リモート画面は閉じられました", true, http.StatusServiceUnavailable},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://tool.test"+tc.path, nil)
		request.Header.Set("Accept-Language", "ja-JP")
		request.Header.Set("Origin", tc.origin)
		response := httptest.NewRecorder()
		s.Handler(func(*http.Request) bool { return tc.authorized }).ServeHTTP(response, request)
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.text) || strings.Contains(response.Body.String(), s.Address()) || s.HasViewer() {
			t.Fatal("本地化改变了认证/来源门禁或暴露内部地址", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Frame-Options") != "SAMEORIGIN" ||
			strings.Contains(response.Header().Get("Content-Security-Policy"), "unsafe-inline") {
			t.Fatal("本地化改变了原安全响应头")
		}
	}
}
