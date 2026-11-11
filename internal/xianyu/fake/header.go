package fake

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const fixtureNickname = "本地测试号"

func loginHeader(nick string) string {
	if nick == "absent" {
		return ""
	}
	avatar := `<img src="/images/1.jpg" width="24" height="24" alt="">`
	name := ""
	switch nick {
	case "valid":
		name = `<div class="nick--fixture">` + fixtureNickname + `</div>`
	case "empty":
		name = "<div class=\"nick--fixture\" style=\"min-height:24px;min-width:40px\"> \n </div>"
	case "login":
		name = `<div class="nick--fixture">登录</div>`
	case "hidden":
		name = `<div class="nick--fixture" style="display:none">` + fixtureNickname + `</div>`
	}
	return `<header id="header"><div class="user-order-container--fixture"><a class="item--fixture" href="/personal">` + avatar + name + `</a></div></header>`
}

func pageWithLoginHeader(source, nick string) string {
	header := loginHeader(nick)
	for _, marker := range []string{"</title>", `<meta charset="utf-8">`} {
		if strings.Contains(source, marker) {
			return strings.Replace(source, marker, marker+header, 1)
		}
	}
	return header + source
}

func loginHeaderState(r *http.Request) (string, bool) {
	nick := r.URL.Query().Get("nick")
	if nick == "" {
		nick = "valid"
	}
	switch nick {
	case "valid", "empty", "login", "hidden", "avatar", "absent":
		return nick, true
	}
	return "", false
}

func (f *Xianyu) fixtureLoginCompleted(r *http.Request) bool {
	f.mu.Lock()
	generation := f.loginGeneration
	f.mu.Unlock()
	cookie, err := r.Cookie("fixture_login_completed")
	return err == nil && cookie.Value == strconv.FormatUint(generation+1, 10)
}

func (f *Xianyu) markFixtureLoginCompleted(w http.ResponseWriter) {
	f.mu.Lock()
	generation := f.loginGeneration
	f.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "fixture_login_completed", Value: strconv.FormatUint(generation+1, 10), Path: "/", HttpOnly: true, Expires: time.Now().Add(30 * 24 * time.Hour)})
}
