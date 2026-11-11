package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
)

func TestWatchCheckedLateRiskWinsOverCallbackCancellation(t *testing.T) {
	r := &Runner{S: &browser.Session{Capture: &browser.Capture{}}}
	checked := make(chan struct{})
	var risk atomic.Bool
	checks := 0
	err := r.watchChecked(t.Context(), func(context.Context) error {
		checks++
		if checks == 2 {
			close(checked)
			return nil
		}
		if risk.Load() {
			return &RiskError{Kind: ErrNeedVerify, Where: "处理期间", Signal: "本地测试"}
		}
		return nil
	}, func(context.Context) error {
		<-checked
		risk.Store(true)
		return context.Canceled
	})
	if !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("迟到验证被回调的普通取消覆盖：%v", err)
	}
}

func TestGuardAlreadyCancelledDoesNotStartWork(t *testing.T) {
	cause := errors.New("运行已关闭")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	called := false
	r := &Runner{}
	err := r.Guard(ctx, "取消的处理", func(context.Context) error { called = true; return nil })
	if called || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("已取消仍开始处理或丢失原因：called=%v err=%v", called, err)
	}
}

func TestGuardLateRiskCancelsBlockedHTTPAndPreventsNextCall(t *testing.T) {
	for _, test := range []struct {
		state string
		want  error
	}{
		{"verify", ErrNeedVerify},
		{"denied", ErrDenied},
		{"login_box", ErrLoginExpired},
		{"environment", ErrEnvBlocked},
	} {
		t.Run(test.state, func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if _, err := r.S.NavigateContext(ctx, f.Home(), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				if calls.Add(1) == 1 {
					close(started)
				}
				<-request.Context().Done()
			}))
			guardCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer func() { cancel(); upstream.Close() }()
			joined, result := make(chan struct{}), make(chan error, 1)
			go func() {
				result <- r.Guard(guardCtx, "阻塞处理", func(workCtx context.Context) error {
					defer close(joined)
					request, err := http.NewRequestWithContext(workCtx, http.MethodPost, upstream.URL, nil)
					if err != nil {
						return err
					}
					response, err := upstream.Client().Do(request)
					if response != nil {
						response.Body.Close()
					}
					return err
				})
			}()
			select {
			case <-started:
			case <-guardCtx.Done():
				t.Fatal("没有进入本地阻塞请求")
			}
			if test.state == "environment" || test.state == "login_box" {
				path := "blocked-mask"
				if test.state == "login_box" {
					path = "login-box"
				}
				if _, err := r.S.NavigateContext(ctx, f.Home()+path, 15*time.Second); err != nil {
					t.Fatal(err)
				}
			} else {
				sendLoginSignal(t, r, ctx, f.Home(), "state="+test.state)
			}
			select {
			case err := <-result:
				if !errors.Is(err, test.want) {
					t.Fatalf("风险没有中断阻塞请求：%v", err)
				}
			case <-guardCtx.Done():
				t.Fatal("风险出现后处理没有结束")
			}
			select {
			case <-joined:
			default:
				t.Fatal("风险返回时阻塞请求还没退出")
			}
			nextCalled := false
			err := r.Guard(ctx, "下一次处理", func(context.Context) error { nextCalled = true; return nil })
			if nextCalled || !errors.Is(err, test.want) || calls.Load() != 1 {
				t.Fatalf("风险后又开始下一次请求：next=%v calls=%d err=%v", nextCalled, calls.Load(), err)
			}
		})
	}
}
