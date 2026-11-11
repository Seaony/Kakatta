package web

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func sendSideSessionExpired(t *testing.T, r *Runner, api string) {
	t.Helper()
	target := strings.TrimSuffix(r.Site.Home, "/") + "/h5/" + api + "/1.0/?token=private-side-token"
	mark := r.S.Capture.Mark()
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool]("fetch("+strconv.Quote(target)+"); true")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.S.Capture.WaitAfter(r.S.Context(), mark, browser.Match{URL: target}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestSideSessionExpiredDoesNotStopLaterOperations(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	var observations, failures int
	r.Log = func(event string, fields map[string]any) {
		if event == "接口登录观察" {
			observations++
			if fields["判定"] != "仅观察" || fields["到达顺序"] == uint64(0) {
				t.Errorf("旁路日志必须注明仅观察和到达顺序：%+v", fields)
			}
		}
		if event == "步骤失败" || event == "风控信号" {
			failures++
		}
	}
	for _, api := range []string{"mtop.taobao.idlemessage.pc.loginuser.get", "mtop.idle.web.user.page.nav"} {
		sendSideSessionExpired(t, r, api)
	}
	for _, step := range []string{"首页停留", "下一步"} {
		if err := r.check(ctx, step); err != nil {
			t.Errorf("旁路失效不得中止普通流程：%v", err)
		}
	}
	if t.Failed() {
		return
	}
	if _, err := r.Search(ctx, "相机"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenDetail(ctx, xianyu.WebLink("fleamarket://item?id=101")); err != nil {
		t.Fatal(err)
	}
	if observations != 2 || failures != 0 {
		t.Fatalf("旁路响应各只观测一次，不能变成失败：观测%d次，失败%d次", observations, failures)
	}
}

func TestOldResponseSignalIsObservedOnceWithoutClearingRisk(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	sendLoginSignal(t, r, ctx, f.Home(), "state=verify")
	var signals int
	r.Log = func(event string, fields map[string]any) {
		if event == "风控信号" && fields["响应序号"] != nil {
			signals++
		}
	}
	for _, step := range []string{"首次检查", "下次检查"} {
		if err := r.checkResponses(step); !errors.Is(err, ErrNeedVerify) {
			t.Fatalf("旧验证码仍未解决，必须继续拦住：%v", err)
		}
	}
	if signals != 1 {
		t.Fatalf("同一响应跨步骤重复记录：%d 次", signals)
	}
}

func TestTargetSessionExpiredStopsImmediately(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	targetSignals := 0
	r.Log = func(event string, fields map[string]any) {
		if event == "风控信号" && fields["判定"] == "目标接口登录失效" {
			targetSignals++
			if fields["响应序号"] == nil || fields["到达顺序"] == uint64(0) || fields["网址"] == nil {
				t.Errorf("目标失效必须保留接口身份：%+v", fields)
			}
		}
	}
	mark := startLoginSignal(t, r, f.Home(), "state=expired")
	_, err := r.waitAPI(ctx, mark, browser.Match{URL: "/h5/mtop.fake.login/"}, "本次目标", 2*time.Second)
	if !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("本次目标失效必须作为停手错误返回：%v", err)
	}
	if targetSignals != 1 {
		t.Fatalf("目标失效必须明确记录一次：%d次", targetSignals)
	}
}

func TestLoginThenOwnProfileDoesNotReplaySideExpiry(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"login-box", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	observedBox := make(chan struct{})
	var once sync.Once
	observations := 0
	r.Log = func(event string, _ map[string]any) {
		if event == "登录框已出现" {
			once.Do(func() { close(observedBox) })
		}
		if event == "接口登录观察" {
			observations++
		}
	}
	for _, api := range []string{"mtop.taobao.idlemessage.pc.loginuser.get", "mtop.idle.web.user.page.nav"} {
		sendSideSessionExpired(t, r, api)
	}
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(finished); result <- r.WaitForLogin(loginCtx, 5*time.Second) }()
	defer func() { cancel(); <-finished }()
	select {
	case <-observedBox:
	case err := <-result:
		t.Fatalf("登录框还在时不能完成登录：%v", err)
	case <-loginCtx.Done():
		t.Fatal("没有识别到可见登录框")
	}
	button, err := r.H.FirstVisible(ctx, "#finish-login", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.H.Click(ctx, button); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("登录框出现后消失应满足既有恢复证据：%v", err)
	}
	if head, err := r.OwnProfile(ctx); err != nil || head.Nick != "测试号" {
		t.Fatalf("扫码后读自己的资料不能重放登录前旁路失效：%+v，%v", head, err)
	}
	if observations != 2 {
		t.Fatalf("扫码前的两个旁路响应不能跨步骤重复观测：%d次", observations)
	}
}

func TestTargetExpiryCannotBeHiddenByLaterSuccess(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	mark := r.S.Capture.Mark()
	sendLoginSignal(t, r, ctx, f.Home(), "state=expired")
	sendLoginSignal(t, r, ctx, f.Home(), "state=success")
	_, err := r.waitAPIResponse(ctx, mark, browser.Match{URL: "/h5/mtop.fake.login/"}, "本次目标", time.Second, time.Millisecond)
	if !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("同次目标较早失效不能被最后的成功响应掩盖：%v", err)
	}
}

func TestTargetExpiryKeepsOperationMarkAfterTokenRetry(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	mark := startLoginSignal(t, r, f.Home(), "state=expired&hold=1")
	sendLoginSignal(t, r, ctx, f.Home(), "state=token_expired")
	retrying := make(chan struct{})
	var once sync.Once
	r.Log = func(event string, fields map[string]any) {
		if event == "接口返回" && fields["结果"] == xianyu.RetTokenExpired.String() {
			once.Do(func() { close(retrying) })
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(finished)
		_, err := r.waitAPI(waitCtx, mark, browser.Match{URL: "/h5/mtop.fake.login/"}, "本次目标", 4*time.Second)
		result <- err
	}()
	defer func() { cancel(); <-finished }()
	select {
	case <-retrying:
	case err := <-result:
		t.Fatalf("应先跳过令牌过期并继续等：%v", err)
	case <-waitCtx.Done():
		t.Fatal("没有等到令牌重发阶段")
	}
	f.ReleaseLoginResponses()
	if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "hold=1"}, time.Second); err != nil {
		t.Fatal(err)
	}
	sendLoginSignal(t, r, ctx, f.Home(), "state=success")
	if err := <-result; !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("等令牌重发不能跳过本次更早请求的迟到失效：%v", err)
	}
}

func TestIgnoredSideExpiryDoesNotDiscardLateRiskBody(t *testing.T) {
	for _, test := range []struct {
		state string
		want  error
	}{{"verify", ErrNeedVerify}, {"denied", ErrDenied}} {
		t.Run(test.state, func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			mark := startLoginSignal(t, r, f.Home(), "state="+test.state+"&hold=1")
			sendSideSessionExpired(t, r, "mtop.idle.web.user.page.nav")
			if err := r.check(ctx, "忽略旁路失效"); err != nil {
				t.Fatal(err)
			}
			f.ReleaseLoginResponses()
			if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "hold=1"}, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			for _, step := range []string{"正文后来到达", "下一次仍须拦住"} {
				if err := r.check(ctx, step); !errors.Is(err, test.want) {
					t.Fatalf("旧请求晚到正文的风险不能被扫描水位丢掉：%v", err)
				}
			}
		})
	}
}

func TestVisibleLoginBoxStopsAutomaticSession(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"login-box", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.check(ctx, "首页登录检查"); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("明确可见的登录框必须拦住普通运行：%v", err)
	}
	if err := r.CheckVerification(ctx); err != nil {
		t.Fatalf("人工登录仍应留在同一画面等扫码：%v", err)
	}
}
