package web

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

type loginDiagnosticCounts struct {
	signals   atomic.Int64
	failures  atomic.Int64
	diagnoses atomic.Int64
}

func watchLoginDiagnostics(r *Runner) *loginDiagnosticCounts {
	counts := &loginDiagnosticCounts{}
	r.Log = func(event string, _ map[string]any) {
		if event == "风控信号" {
			counts.signals.Add(1)
		}
		if event == "步骤失败" {
			counts.failures.Add(1)
		}
	}
	r.Diagnose = func(context.Context, Diagnostic) error { counts.diagnoses.Add(1); return nil }
	return counts
}

func TestManualLoginBoxDoesNotReportFailure(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	r.Site.Home = f.Home() + "login-box"
	counts := watchLoginDiagnostics(r)
	if err := r.OpenLogin(ctx); err != nil {
		t.Fatal(err)
	}
	observedBox := make(chan struct{})
	var once sync.Once
	record := r.Log
	r.Log = func(event string, fields map[string]any) {
		record(event, fields)
		if event == "登录框已出现" {
			once.Do(func() { close(observedBox) })
		}
	}
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(finished); result <- r.WaitForLogin(loginCtx, 5*time.Second) }()
	defer func() { cancel(); <-finished }()
	select {
	case <-observedBox:
	case err := <-result:
		t.Fatalf("扫码框仍可见，不能结束登录：%v", err)
	case <-loginCtx.Done():
		t.Fatal("没有观察到登录框")
	}
	// 留过一次轮询，再模拟用户完成扫码。
	timer := time.NewTimer(1100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case err := <-result:
		t.Fatalf("等待扫码时流程提前结束：%v", err)
	case <-loginCtx.Done():
		t.Fatal("人工登录检查超时")
	}
	button, err := r.H.FirstVisible(ctx, "#finish-login", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.H.Click(ctx, button); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("用户完成扫码后应成功：%v", err)
	}
	if counts.signals.Load() != 0 || counts.failures.Load() != 0 || counts.diagnoses.Load() != 0 {
		t.Fatalf("正常等待扫码不能记失败：风控%d，步骤失败%d，诊断%d", counts.signals.Load(), counts.failures.Load(), counts.diagnoses.Load())
	}
}

func TestAutomaticLoginBoxAndConfirmationStillReportFailure(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"login-box", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name string
		run  func() error
	}{{"自动流程", func() error { return r.check(ctx, "自动登录检查") }}, {"人工确认完成", func() error { return r.ConfirmVerification(ctx) }}} {
		t.Run(check.name, func(t *testing.T) {
			counts := watchLoginDiagnostics(r)
			if err := check.run(); !errors.Is(err, ErrLoginExpired) {
				t.Fatalf("登录框仍可见必须停手：%v", err)
			}
			if counts.failures.Load() == 0 || counts.diagnoses.Load() == 0 {
				t.Fatalf("实际未登录仍需记录诊断：步骤失败%d，诊断%d", counts.failures.Load(), counts.diagnoses.Load())
			}
		})
	}
}

func TestManualLoginDiagnosticsKeepRefusalAndVerification(t *testing.T) {
	for _, test := range []struct {
		path string
		want error
	}{{"punish?action=deny", ErrDenied}, {"blocked-mask", ErrEnvBlocked}, {"punish?action=captcha", nil}} {
		t.Run(test.path, func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.Home()+test.path, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](`document.body.insertAdjacentHTML('beforeend', '<iframe id="alibaba-login-box" style="width:220px;height:120px"></iframe>'); true`)); err != nil {
				t.Fatal(err)
			}
			counts := watchLoginDiagnostics(r)
			if _, err := r.VerificationPageNeedsRefresh(ctx); !errors.Is(err, test.want) {
				t.Fatalf("人工窗口不能让登录框遮住拒绝，验证码仍留给用户：%v", err)
			}
			limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			err := r.WaitForLogin(limited, time.Second)
			cancel()
			want := test.want
			if want == nil {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("拒绝必须立即结束、验证码必须继续等用户：%v", err)
			}
			if counts.signals.Load() == 0 || counts.failures.Load() == 0 || counts.diagnoses.Load() == 0 {
				t.Fatalf("真实风险仍须记录：风控%d，步骤失败%d，诊断%d", counts.signals.Load(), counts.failures.Load(), counts.diagnoses.Load())
			}
		})
	}
}
