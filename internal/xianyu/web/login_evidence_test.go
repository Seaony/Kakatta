package web

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"
)

func loginEvidenceRunner(t *testing.T, path string) (*Runner, context.Context) {
	t.Helper()
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	original := rules
	rules.Timeouts = maps.Clone(rules.Timeouts)
	rules.Timeouts["login_poll"] = 20
	t.Cleanup(func() { rules = original })
	r.Site.Home = f.Home() + path
	if err := r.OpenLogin(ctx); err != nil {
		t.Fatal(err)
	}
	return r, ctx
}

func waitForLoginEvidence(t *testing.T, r *Runner, ctx context.Context) (<-chan error, <-chan struct{}) {
	t.Helper()
	observed := make(chan struct{})
	var once sync.Once
	record := r.Log
	r.Log = func(event string, fields map[string]any) {
		record(event, fields)
		if event == "登录框已出现" {
			once.Do(func() { close(observed) })
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(finished); result <- r.WaitForLogin(waitCtx, 4*time.Second) }()
	t.Cleanup(func() { cancel(); <-finished })
	return result, observed
}

func requireLoginStillPending(t *testing.T, result <-chan error, duration time.Duration) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case err := <-result:
		t.Fatalf("缺少本会话的新页面证据，登录不能提前完成：%v", err)
	case <-timer.C:
	}
}

func requireLoginBoxObserved(t *testing.T, result <-chan error, observed <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-observed:
	case err := <-result:
		t.Fatalf("本会话还没观察到登录框，不能完成：%v", err)
	case <-timer.C:
		t.Fatal("没有观察到延迟出现的登录框")
	}
}

func clickLoginEvidenceButton(t *testing.T, r *Runner, ctx context.Context, selector string) {
	t.Helper()
	button, err := r.H.FirstVisible(ctx, selector, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.H.Click(ctx, button); err != nil {
		t.Fatal(err)
	}
}

func finishFakeLogin(t *testing.T, r *Runner, ctx context.Context) {
	t.Helper()
	if _, err := r.H.FirstVisible(ctx, rules.Selectors.LoginBox, time.Second); err != nil {
		t.Fatal(err)
	}
	if loggedIn, err := r.LoggedIn(ctx); err != nil || loggedIn {
		t.Fatalf("正式本地检查必须先看到仍可见的登录框：%v %v", loggedIn, err)
	}
	result, _ := waitForLoginEvidence(t, r, ctx)
	clickLoginEvidenceButton(t, r, ctx, "#finish-login")
	if err := <-result; err != nil {
		t.Fatalf("本次扫码和新接口请求后应完成登录：%v", err)
	}
}

func completeFakeLogin(t *testing.T, r *Runner, ctx context.Context, home string) {
	t.Helper()
	if err := r.S.Navigate(home+"login-box", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	finishFakeLogin(t, r, ctx)
}

func TestDelayedLoginBoxCannotUseStaleCookie(t *testing.T) {
	r, ctx := loginEvidenceRunner(t, "fixture/login-delayed?completion=none")
	if present, err := r.HasLoginCookieContext(ctx); err != nil || !present {
		t.Fatalf("夹具必须保留旧登录标记：存在%v，错误%v", present, err)
	}
	result, observed := waitForLoginEvidence(t, r, ctx)
	requireLoginStillPending(t, result, 250*time.Millisecond)
	requireLoginBoxObserved(t, result, observed)
	requireLoginStillPending(t, result, 150*time.Millisecond)
}

func TestLoginBoxDisappearanceAloneCannotComplete(t *testing.T) {
	r, ctx := loginEvidenceRunner(t, "fixture/login-delayed?completion=none")
	if _, err := r.H.FirstVisible(ctx, rules.Selectors.LoginBox, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	result, observed := waitForLoginEvidence(t, r, ctx)
	requireLoginBoxObserved(t, result, observed)
	clickLoginEvidenceButton(t, r, ctx, "#finish-login")
	requireLoginStillPending(t, result, 250*time.Millisecond)
}

func TestLoginNeedsFreshPageEvidenceAfterBox(t *testing.T) {
	for _, completion := range []string{"navigation", "mtop"} {
		for _, cookie := range []string{"stale", "expired"} {
			t.Run(completion+"/"+cookie, func(t *testing.T) {
				r, ctx := loginEvidenceRunner(t, "fixture/login-delayed?completion="+completion+"&cookie="+cookie)
				if _, err := r.H.FirstVisible(ctx, rules.Selectors.LoginBox, 2*time.Second); err != nil {
					t.Fatal(err)
				}
				if present, err := r.HasLoginCookieContext(ctx); err != nil || present != (cookie == "stale") {
					t.Fatalf("夹具登录标记不符合预期：存在%v，错误%v", present, err)
				}
				result, observed := waitForLoginEvidence(t, r, ctx)
				requireLoginBoxObserved(t, result, observed)
				clickLoginEvidenceButton(t, r, ctx, "#finish-login")
				requireLoginStillPending(t, result, 150*time.Millisecond)
				beforeNav, beforeRequest := r.S.NavigationMark(), r.S.Capture.Mark()
				clickLoginEvidenceButton(t, r, ctx, "#continue-login")
				select {
				case err := <-result:
					if err != nil {
						t.Fatalf("本会话见框、关框后已有新页面证据，应完成登录：%v", err)
					}
				case <-ctx.Done():
					t.Fatal("新页面证据后仍未完成登录")
				}
				if completion == "navigation" && r.S.NavigationMark() <= beforeNav {
					t.Fatal("成功前必须实际观察到新的主框架导航")
				}
				if completion == "mtop" && !r.S.Capture.SentAfter(beforeRequest, r.Site.mtop()) {
					t.Fatal("成功前必须实际观察到本次新的接口发起")
				}
			})
		}
	}
}

func TestCookieWithoutNewPageEvidenceCannotComplete(t *testing.T) {
	for _, cookie := range []string{"stale", "expired"} {
		t.Run(cookie, func(t *testing.T) {
			r, ctx := loginEvidenceRunner(t, "fixture/login-cookie-only?cookie="+cookie)
			if present, err := r.HasLoginCookieContext(ctx); err != nil || present != (cookie == "stale") {
				t.Fatalf("夹具登录标记不符合预期：存在%v，错误%v", present, err)
			}
			result, _ := waitForLoginEvidence(t, r, ctx)
			requireLoginStillPending(t, result, 250*time.Millisecond)
		})
	}
}

func TestRepeatedOpenLoginCannotBorrowPreviousSessionEvidence(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	r.Site.Home = f.Home() + "login-box"
	if err := r.OpenLogin(ctx); err != nil {
		t.Fatal(err)
	}
	finishFakeLogin(t, r, ctx)
	r.Site.Home = f.Home() + "fixture/login-delayed?completion=none"
	if err := r.OpenLogin(ctx); err != nil {
		t.Fatal(err)
	}
	result, observed := waitForLoginEvidence(t, r, ctx)
	requireLoginStillPending(t, result, 250*time.Millisecond)
	requireLoginBoxObserved(t, result, observed)
	requireLoginStillPending(t, result, 150*time.Millisecond)
}
