package web

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
)

func sendLoginSignal(t *testing.T, r *Runner, ctx context.Context, home, query string) {
	t.Helper()
	mark := startLoginSignal(t, r, home, query)
	if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "/h5/mtop.fake.login/"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func startLoginSignal(t *testing.T, r *Runner, home, query string) uint64 {
	t.Helper()
	mark := r.S.Capture.Mark()
	js := "fetch(" + strconv.Quote(home+"h5/mtop.fake.login/1.0/?"+query) + "); true"
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](js)); err != nil {
		t.Fatal(err)
	}
	return mark
}

func TestLoginRejectsDenialAfterEarlierVerification(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	sendLoginSignal(t, r, ctx, f.Home(), "state=verify")
	sendLoginSignal(t, r, ctx, f.Home(), "state=denied")
	if err := r.CheckVerification(ctx); !errors.Is(err, ErrDenied) {
		t.Fatalf("人工窗口也必须识别拒绝：%v", err)
	}
	if err := r.WaitForLogin(ctx, 5*time.Second); !errors.Is(err, ErrDenied) {
		t.Fatalf("早期验证码不能遮住后来的拒绝：%v", err)
	}
	if err := r.checkResponses("普通检查"); !errors.Is(err, ErrDenied) {
		t.Fatalf("普通流程同样要识别拒绝：%v", err)
	}
}

func otherLoginSuccess(t *testing.T, r *Runner, ctx context.Context, home string) {
	t.Helper()
	mark := r.S.Capture.Mark()
	js := "fetch(" + strconv.Quote(home+"h5/mtop.idle.web.user.page.head/1.0/") + "); true"
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](js)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.S.Capture.WaitAfter(ctx, mark, r.Site.userHeadAPI(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestLoginRequiresCurrentBoxBeyondCookieAndSuccess(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	otherLoginSuccess(t, r, ctx, f.Home())
	sendLoginSignal(t, r, ctx, f.Home(), "state=expired")
	limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	err := r.WaitForLogin(limited, 5*time.Second)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("旧Cookie和失效前的成功不能证明恢复：%v", err)
	}
	otherLoginSuccess(t, r, ctx, f.Home())
	limited, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
	err = r.WaitForLogin(limited, 5*time.Second)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("失效后的旁路成功仍不能代替本次扫码：%v", err)
	}
	completeFakeLogin(t, r, ctx, f.Home())
	if _, err := r.OwnProfile(ctx); err != nil {
		t.Fatalf("恢复后不能重放已处理失效：%v", err)
	}
}

func TestLoginWithoutSessionExpiredStillNeedsCurrentPageEvidence(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	sendLoginSignal(t, r, ctx, f.Home(), "state=verify")
	limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	err := r.WaitForLogin(limited, 5*time.Second)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("没有失效响应也不能把旧标记当作本次扫码：%v", err)
	}
	completeFakeLogin(t, r, ctx, f.Home())
	if _, err := r.OwnProfile(ctx); err != nil {
		t.Fatalf("已处理的验证码不应重放：%v", err)
	}
}

func TestLoginFrameDisappearanceAndNewMtopRecover(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"login-box", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	sendLoginSignal(t, r, ctx, f.Home(), "state=expired")
	observed := make(chan struct{})
	var once sync.Once
	r.Log = func(event string, _ map[string]any) {
		if event == "登录框已出现" {
			once.Do(func() { close(observed) })
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(finished); result <- r.WaitForLogin(waitCtx, 5*time.Second) }()
	defer func() { cancel(); <-finished }()
	select {
	case <-observed:
	case err := <-result:
		t.Fatalf("扫码前不应完成登录：%v", err)
	case <-waitCtx.Done():
		t.Fatal("没有观察到登录框")
	}
	button, err := r.H.FirstVisible(ctx, "#finish-login", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	before := r.S.Capture.Mark()
	if err := r.H.Click(ctx, button); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("登录框出现后消失并有新请求应恢复：%v", err)
	}
	if after := r.S.Capture.Mark(); after <= before {
		t.Fatalf("假扫码必须实际发出新mtop请求：%d→%d", before, after)
	}
	if _, err := r.OwnProfile(ctx); err != nil {
		t.Fatalf("完成扫码后不能重放失效信号：%v", err)
	}
}

func TestLoginEnvironmentBlockIsNotHiddenByVerification(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	sendLoginSignal(t, r, ctx, f.Home(), "state=verify")
	if err := r.S.Navigate(f.Home()+"blocked-mask", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckVerification(ctx); !errors.Is(err, ErrEnvBlocked) {
		t.Fatalf("人工窗口也必须识别环境拒绝：%v", err)
	}
	if err := r.WaitForLogin(ctx, 5*time.Second); !errors.Is(err, ErrEnvBlocked) {
		t.Fatalf("环境拒绝不能被验证码遮住：%v", err)
	}
}

func TestVerificationKeepsDenialFromRequestStartedBeforeTakeover(t *testing.T) {
	for _, header := range []bool{false, true} {
		t.Run(strconv.FormatBool(header), func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			query := "state=denied&hold=1"
			if header {
				query += "&punish_header=1"
			}
			mark := startLoginSignal(t, r, f.Home(), query)
			match := browser.Match{URL: "/h5/mtop.fake.login/"}
			deadline := time.Now().Add(5 * time.Second)
			for {
				ready := r.S.Capture.SentAfter(mark, match)
				if header {
					ready = len(r.S.Capture.HeadersAfter(mark, match)) > 0
				}
				if ready {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("请求未进入预期阶段")
				}
				time.Sleep(5 * time.Millisecond)
			}
			r.BeginVerification()
			f.ReleaseLoginResponses()
			if _, err := r.S.Capture.WaitAfter(ctx, mark, match, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			if err := r.ConfirmVerification(ctx); !errors.Is(err, ErrDenied) {
				t.Fatalf("接手前发出的请求，后来才返回拒绝也不能忽略：%v", err)
			}
		})
	}
}

func TestLoginSuccessDoesNotDiscardInFlightDenial(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"login-box", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	mark := startLoginSignal(t, r, f.Home(), "state=denied&hold=1")
	finishFakeLogin(t, r, ctx)
	f.ReleaseLoginResponses()
	if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "state=denied&hold=1"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := r.OwnProfile(ctx); !errors.Is(err, ErrDenied) {
		t.Fatalf("登录完成后仍应拦住在途请求的拒绝：%v", err)
	}
}

func TestLoginOldResponseCompletionCannotReplaceCurrentScan(t *testing.T) {
	for _, late := range []string{"expired", "success"} {
		t.Run(late, func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.Home()+"login-box", 15*time.Second); err != nil {
				t.Fatal(err)
			}
			r.BeginVerification()
			mark := startLoginSignal(t, r, f.Home(), "state="+late+"&hold=1")
			if late == "expired" {
				otherLoginSuccess(t, r, ctx, f.Home())
			} else {
				sendLoginSignal(t, r, ctx, f.Home(), "state=expired")
			}
			f.ReleaseLoginResponses()
			if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "state=" + late}, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			err := r.WaitForLogin(limited, 5*time.Second)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("旧响应无论谁先完成，都不能让仍可见的登录框算成功：%v", err)
			}
			finishFakeLogin(t, r, ctx)
		})
	}
}
