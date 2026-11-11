package web

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
)

func TestOpenLoginAlreadyCancelledKeepsCause(t *testing.T) {
	cause := errors.New("用户关闭登录窗口")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	r := &Runner{}
	if err := r.OpenLogin(ctx); !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("打开前取消必须原样外传，不触碰浏览器或额度：%v", err)
	}
}

func TestWatchCheckedCancellationJoinsWorkAndKeepsCause(t *testing.T) {
	cause := errors.New("搜索条件已改变")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	r := &Runner{S: &browser.Session{Capture: &browser.Capture{}}}
	started, joined := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- r.watchChecked(ctx, contextError, func(waitCtx context.Context) error {
			close(started)
			<-waitCtx.Done()
			close(joined)
			return waitCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("没有开始本地等待")
	}
	cancel(cause)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("取消的原始原因丢失：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后没有结束")
	}
	select {
	case <-joined:
	default:
		t.Fatal("返回时仍有操作在执行")
	}
}

func TestWatchCheckedRefusalCancelsAndJoinsWork(t *testing.T) {
	r := &Runner{S: &browser.Session{Capture: &browser.Capture{}}}
	joined := make(chan struct{})
	checks := 0
	err := r.watchChecked(t.Context(), func(context.Context) error {
		checks++
		if checks >= 2 {
			return &RiskError{Kind: ErrDenied, Where: "导航期间", Signal: "本地测试"}
		}
		return nil
	}, func(waitCtx context.Context) error {
		<-waitCtx.Done()
		close(joined)
		return waitCtx.Err()
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("直接拒绝被操作取消覆盖：%v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("拒绝返回时仍有操作在执行")
	}
}

func TestOpenLoginKeepsExpiredAndVerificationForUser(t *testing.T) {
	for _, test := range []struct {
		name, path string
		ordinary   error
		login      error
	}{
		{"登录页", "mini_login", ErrLoginExpired, nil},
		{"验证码", "punish?action=captcha", ErrNeedVerify, nil},
		{"直接拒绝", "punish?action=deny", ErrDenied, ErrDenied},
		{"环境异常", "blocked-mask", ErrEnvBlocked, ErrEnvBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFake(t)
			r, budget, ctx := startRunner(t, f)
			r.Site.Home = f.Home() + test.path
			if err := r.OpenLogin(ctx); !errors.Is(err, test.login) {
				t.Fatalf("人工首页分类错误：%v", err)
			}
			if len(budget.got) != 1 || budget.got[0] != "search:登录首页" {
				t.Fatalf("登录首页只应申请一次搜索访问：%v", budget.got)
			}
			if err := r.check(ctx, "普通运行检查"); !errors.Is(err, test.ordinary) {
				t.Fatalf("人工登录不能放宽普通运行：%v", err)
			}
		})
	}
}

func TestOpenLoginKeepsSideExpiryFreshnessEvidence(t *testing.T) {
	f := newFake(t)
	r, budget, ctx := startRunner(t, f)
	if _, err := r.S.NavigateContext(ctx, f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	sendLoginSignal(t, r, ctx, f.Home(), "state=expired")
	if err := r.OpenHome(ctx); err != nil {
		t.Fatalf("旁路失效不能中止普通首页：%v", err)
	}
	if err := r.OpenLogin(ctx); err != nil {
		t.Fatalf("同一次人工登录必须留给用户处理失效：%v", err)
	}
	if len(budget.got) != 2 {
		t.Fatalf("普通首页和人工登录各计一次实际访问：%v", budget.got)
	}
	limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	err := r.WaitForLogin(limited, 5*time.Second)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("打开人工首页不能用旧Cookie确认已恢复：%v", err)
	}
	otherLoginSuccess(t, r, ctx, f.Home())
	if err := r.WaitForLogin(ctx, 5*time.Second); err != nil {
		t.Fatalf("仍按第67条允许失效后的任意接口成功恢复：%v", err)
	}
}

func TestOpenLoginStopsPriorDenialBeforeAccess(t *testing.T) {
	f := newFake(t)
	r, budget, ctx := startRunner(t, f)
	if _, err := r.S.NavigateContext(ctx, f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	sendLoginSignal(t, r, ctx, f.Home(), "state=denied")
	if err := r.OpenLogin(ctx); !errors.Is(err, ErrDenied) {
		t.Fatalf("人工登录也不能越过直接拒绝：%v", err)
	}
	if len(budget.got) != 0 {
		t.Fatalf("拒绝之后不应再申请首页访问：%v", budget.got)
	}
}
