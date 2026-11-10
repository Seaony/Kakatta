package web

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoginStateStopsBeforeReadsWhenCancelled(t *testing.T) {
	cause := errors.New("用户终止本次登录")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	r := &Runner{}
	present, err := r.LoggedIn(ctx)
	if present || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("取消后仍读取登录状态或丢失原因：%v %v", present, err)
	}
}

func TestReadHelpersPreserveCallerCancellation(t *testing.T) {
	cause := errors.New("本次处理已取消")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	r := &Runner{}
	for _, test := range []struct {
		name string
		read func() error
	}{
		{"Cookie名称检查", func() error { _, err := r.HasLoginCookieContext(ctx); return err }},
		{"运行前Cookie检查", func() error { return r.RequireLoginCookieContext(ctx) }},
		{"登录完成检查", func() error { return r.RequireLoggedIn(ctx, "本地读取") }},
		{"搜索框选择", func() error { _, _, err := r.findSearchInput(ctx); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.read(); !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Fatalf("取消后仍开始读取或丢失取消原因：%v", err)
			}
		})
	}
	if facts := r.InputFacts(ctx); facts != nil {
		t.Fatalf("已取消的诊断不应再查询页面：%v", facts)
	}
}

func TestCookieContextKeepsLegacyAndNameOnlyBehavior(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if present, err := r.HasLoginCookieContext(ctx); err != nil || present {
		t.Fatalf("登录前不应有指定名称：present=%v err=%v", present, err)
	}
	if err := r.RequireLoginCookieContext(ctx); err != nil {
		t.Fatalf("缺少尚未确认的Cookie名称只警告：%v", err)
	}
	if _, err := r.S.NavigateContext(ctx, f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		read func() (bool, error)
	}{
		{"带上下文入口", func() (bool, error) { return r.HasLoginCookieContext(ctx) }},
		{"兼容入口", r.HasLoginCookie},
	} {
		t.Run(test.name, func(t *testing.T) {
			if present, err := test.read(); err != nil || !present {
				t.Fatalf("同名Cookie的检查语义改变：present=%v err=%v", present, err)
			}
		})
	}
	if err := r.RequireLoginCookie(); err != nil {
		t.Fatalf("兼容预检入口不可用：%v", err)
	}
	cause := errors.New("取消局部读取")
	cancelled, cancel := context.WithCancelCause(ctx)
	cancel(cause)
	if present, err := r.HasLoginCookieContext(cancelled); present || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("局部取消未保留：present=%v err=%v", present, err)
	}
	if _, err := r.S.URLContext(ctx); err != nil {
		t.Fatalf("取消局部读取不应关闭会话：%v", err)
	}
}
