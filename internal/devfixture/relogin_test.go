package devfixture_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/devfixture"
)

func TestSessionExpiredRequiresFreshFixtureLoginCompletion(t *testing.T) {
	f := devfixture.New(t.Context(), "http://127.0.0.1:19090")
	t.Cleanup(f.Close)
	control := httptest.NewServer(f.Handler())
	t.Cleanup(control.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second}
	home, err := url.Parse(f.Metadata().Site.Home)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		response, err := client.Get(home.String() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatal("假站点页面读取失败", response.StatusCode, err)
		}
		return string(body)
	}
	command := func(body string) {
		t.Helper()
		if status, _ := fixtureRequest(t, http.MethodPost, control.URL+"/control", body); status != http.StatusOK {
			t.Fatal("假站点状态切换失败", status)
		}
	}
	gate := func(body string) string {
		t.Helper()
		_, rest, found := strings.Cut(body, "fetch('/fixture/login-gate?id=")
		id, _, complete := strings.Cut(rest, "'")
		if !found || !complete || id == "" {
			t.Fatal("失效后首页没有本次扫码登录框和完成门禁")
		}
		return "fixture/login-gate?id=" + id
	}
	loggedIn := func(path string) {
		t.Helper()
		body := get(path)
		if !strings.Contains(body, `class="nick--fixture">本地测试号</div>`) || strings.Contains(body, `id="alibaba-login-box"`) {
			t.Fatal("已完成的假扫码没有在普通页面保持登录", path)
		}
	}
	first := gate(get(""))
	command(`{"action":"login_complete"}`)
	get(first)
	loggedIn("")
	command(`{"action":"risk","mode":"expired"}`)
	command(`{"action":"risk","mode":"none"}`)
	markerPresent := false
	for _, cookie := range jar.Cookies(home) {
		markerPresent = markerPresent || cookie.Name == "fixture_login_completed"
	}
	if !markerPresent {
		t.Fatal("本用例必须保留浏览器里的旧假登录标记")
	}
	body := get("")
	if strings.Contains(body, `class="nick--fixture">本地测试号</div>`) || !strings.Contains(body, `class="nick--fixture">登录</div>`) || !strings.Contains(body, `id="alibaba-login-box"`) {
		t.Fatal("SessionExpired后清除风险，旧标记仍让首页冒充已经登录")
	}
	second := gate(body)
	if first == second {
		t.Fatal("重新登录必须建立新的扫码门禁")
	}
	command(`{"action":"login_complete"}`)
	get(second)
	for _, path := range []string{"", "personal", "search?q=fixture", "item?id=101"} {
		loggedIn(path)
	}
	for _, mode := range []string{"verify", "none"} {
		command(`{"action":"risk","mode":"` + mode + `"}`)
		loggedIn("")
	}
	if f.State()["login_completions"] != int64(2) {
		t.Fatal("风险恢复不能替用户再完成一次扫码")
	}
}
