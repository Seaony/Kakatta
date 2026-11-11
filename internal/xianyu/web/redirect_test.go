package web

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
)

func startRedirectSignal(t *testing.T, r *Runner, home, query string) uint64 {
	t.Helper()
	mark := r.S.Capture.Mark()
	js := "fetch(" + strconv.Quote(home+"h5/mtop.fake.redirect/1.0/?"+query) + ", {method:'POST'}); true"
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](js)); err != nil {
		t.Fatal(err)
	}
	return mark
}

func TestAPIRedirectRetainsRiskClassification(t *testing.T) {
	for _, state := range []string{"success", "denied", "denied_header"} {
		t.Run(state, func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.LoginURL(), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			r.BeginVerification()
			mark := startRedirectSignal(t, r, f.Home(), "state="+state)
			response, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "/fixture/punish"}, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if response.MainDocument || response.Method != "GET" || !strings.Contains(response.URL, "action=deny") || response.RequestMethod != "POST" || !strings.Contains(response.RequestURL, "/h5/mtop.fake.redirect/") {
				t.Fatalf("必须是POST转GET的后台响应：%+v", response)
			}
			original := browser.Match{URL: "/h5/mtop.fake.redirect/", Method: "POST"}
			if !r.S.Capture.SentAfter(mark, original) || len(r.S.Capture.HeadersAfter(mark, original)) != 1 || len(r.S.Capture.DoneAfter(mark, original)) != 1 {
				t.Fatal("原接口POST匹配必须保留重定向后的头和正文")
			}
			var want error
			if strings.HasPrefix(state, "denied") {
				want = ErrDenied
			}
			for name, err := range map[string]error{
				"普通风控": r.check(ctx, "重定向后"),
				"人工轮询": r.CheckVerification(ctx),
				"人工确认": r.ConfirmVerification(ctx),
			} {
				if !errors.Is(err, want) {
					t.Fatalf("%s的风险分类错误：得到%v，应为%v", name, err, want)
				}
			}
			loginWant := want
			if loginWant == nil {
				loginWant = ErrLoginTimeout
			}
			if err := r.WaitForLogin(ctx, time.Second); !errors.Is(err, loginWant) {
				t.Fatalf("普通接口成功不能代替扫码，拒绝仍须停手：得到%v，应为%v", err, loginWant)
			}
			if _, err := r.waitAPI(ctx, mark, original, "等待原POST接口", time.Second); !errors.Is(err, want) {
				t.Fatalf("按原POST等待不能漏掉重定向响应或风险：%v", err)
			}
		})
	}
}

func TestAPIRedirectPunishmentHeaderStopsBeforeBody(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	mark := startRedirectSignal(t, r, f.Home(), "state=header&hold=1")
	deadline := time.Now().Add(5 * time.Second)
	for len(r.S.Capture.HeadersAfter(mark, browser.Match{URL: "/fixture/punish"})) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("没有收到重定向后的处罚头")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := r.check(ctx, "重定向处罚头"); !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("后台最终网址不能当主框架拒绝，处罚头应返回验证：%v", err)
	}
	signals, _, err := r.loginSignals()
	if err != nil || len(signals) != 1 || !signals[0].header {
		t.Fatalf("人工登录扫描必须保留后台重定向处罚头：%+v %v", signals, err)
	}
	if err := r.CheckVerification(ctx); err != nil {
		t.Fatalf("后台验证仍留给人工处理：%v", err)
	}
	if err := r.ConfirmVerification(ctx); !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("正文仍在途时不能确认重定向后的验证：%v", err)
	}
	original := browser.Match{URL: "/h5/mtop.fake.redirect/", Method: "POST"}
	if _, err := r.waitAPI(ctx, mark, original, "等待重定向处罚头", time.Second); !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("普通等待也应被处罚头立即打断：%v", err)
	}
	f.ReleaseLoginResponses()
	if _, err := r.S.Capture.WaitAfter(ctx, mark, original, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}
