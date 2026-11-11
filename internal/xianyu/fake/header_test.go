package fake

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func readHeaderFixture(t *testing.T, client *http.Client, target string) (string, *http.Response) {
	t.Helper()
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("读取用户区夹具失败：HTTP %d，错误 %v", response.StatusCode, err)
	}
	return string(body), response
}

func TestLoginHeaderFixtureStatesDoNotAuthenticate(t *testing.T) {
	f := Start()
	t.Cleanup(f.Close)
	for _, state := range []string{"valid", "empty", "login", "hidden", "avatar", "absent"} {
		t.Run(state, func(t *testing.T) {
			body, response := readHeaderFixture(t, http.DefaultClient, f.Home()+"fixture/login-header?nick="+state)
			if len(response.Header.Values("Set-Cookie")) != 0 {
				t.Fatal("静态用户区结构不能设置登录标记")
			}
			if state == "absent" {
				if strings.Contains(body, `id="header"`) {
					t.Fatal("缺失场景不能补出用户区")
				}
				return
			}
			for _, fragment := range []string{`id="header"`, `class="user-order-container--fixture"`, `class="item--fixture"`, `src="/images/1.jpg"`} {
				if !strings.Contains(body, fragment) {
					t.Fatalf("用户区缺少固定结构：%s", fragment)
				}
			}
			if state == "avatar" && strings.Contains(body, `class="nick--fixture"`) {
				t.Fatal("只有头像的场景不能产生昵称节点")
			}
			if state == "valid" && !strings.Contains(body, `<div class="nick--fixture">本地测试号</div>`) {
				t.Fatal("有效昵称必须直接放在用户区昵称节点里")
			}
			if state == "login" && !strings.Contains(body, `<div class="nick--fixture">登录</div>`) {
				t.Fatal("未登录时昵称节点必须直接写登录")
			}
			if state == "empty" && !strings.Contains(body, "<div class=\"nick--fixture\" style=\"min-height:24px;min-width:40px\"> \n </div>") {
				t.Fatal("空昵称场景必须保留空白文本节点")
			}
			if state == "hidden" && !strings.Contains(body, `class="nick--fixture" style="display:none"`) {
				t.Fatal("隐藏场景只隐藏昵称，用户区和头像仍保留")
			}
		})
	}
}

func TestOnlyCompletedFixtureLoginPersistsPositiveHeader(t *testing.T) {
	f := Start()
	t.Cleanup(f.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	readHeaderFixture(t, client, f.LoginURL())
	paths := []string{"", "personal", "search?q=fixture", "item?id=101"}
	check := func(completed bool) {
		t.Helper()
		for _, path := range paths {
			body, _ := readHeaderFixture(t, client, f.Home()+path)
			if strings.Contains(body, loginHeader("valid")) != completed || strings.Contains(body, loginHeader("login")) == completed {
				t.Fatalf("普通页面用户区必须跟随已完成的假扫码，路径%s，已完成%v", path, completed)
			}
		}
	}
	check(false)
	readHeaderFixture(t, client, f.Home()+"h5/mtop.fake.login/1.0/?state=success&after_scan=1&nick=valid")
	check(true)
}

func TestInvalidNickCannotMarkFixtureLoginCompleted(t *testing.T) {
	f := Start()
	t.Cleanup(f.Close)
	for _, state := range []string{"empty", "login", "hidden", "avatar", "absent"} {
		t.Run(state, func(t *testing.T) {
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Jar: jar}
			_, response := readHeaderFixture(t, client, f.Home()+"h5/mtop.fake.login/1.0/?state=success&after_scan=1&nick="+state)
			if len(response.Header.Values("Set-Cookie")) != 0 {
				t.Fatal("没有可用昵称的完成响应不能保存正面身份")
			}
			body, _ := readHeaderFixture(t, client, f.Home())
			if !strings.Contains(body, loginHeader("login")) || strings.Contains(body, loginHeader("valid")) {
				t.Fatal("无效昵称不能让后续普通首页冒充已登录")
			}
		})
	}
}
