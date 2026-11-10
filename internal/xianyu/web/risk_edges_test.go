package web

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
)

func TestCombinedRiskKeepsRefusalAheadOfVerification(t *testing.T) {
	readErr := errors.New("页面读取失败")
	for _, test := range []struct {
		name     string
		response error
		page     error
		want     error
	}{
		{"接口验证和页面拒绝", ErrNeedVerify, ErrDenied, ErrDenied},
		{"接口失效和页面拒绝", ErrLoginExpired, ErrDenied, ErrDenied},
		{"接口验证和环境异常", ErrNeedVerify, ErrEnvBlocked, ErrEnvBlocked},
		{"接口失效和环境异常", ErrLoginExpired, ErrEnvBlocked, ErrEnvBlocked},
		{"接口拒绝和页面验证", ErrDenied, ErrNeedVerify, ErrDenied},
		{"接口拒绝和页面读取失败", ErrDenied, readErr, ErrDenied},
		{"页面读取失败不能当作待验证", ErrNeedVerify, readErr, readErr},
		{"读取取消原样返回", ErrNeedVerify, context.Canceled, context.Canceled},
		{"只有接口验证", ErrNeedVerify, nil, ErrNeedVerify},
		{"只有接口失效", ErrLoginExpired, nil, ErrLoginExpired},
		{"只有页面验证", nil, ErrNeedVerify, ErrNeedVerify},
		{"没有风险", nil, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := combinedRisk(test.response, test.page)
			if !errors.Is(got, test.want) {
				t.Fatalf("检查结果=%v，应该是%v", got, test.want)
			}
		})
	}
}

func TestOrdinaryCheckDoesNotHidePageRefusal(t *testing.T) {
	for _, responseState := range []string{"verify", "expired"} {
		for _, currentPage := range []struct {
			path string
			want error
		}{
			{"punish?action=deny", ErrDenied},
			{"blocked-mask", ErrEnvBlocked},
		} {
			t.Run(responseState+"/"+currentPage.path, func(t *testing.T) {
				f := newFake(t)
				r, budget, ctx := startRunner(t, f)
				if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
					t.Fatal(err)
				}
				sendLoginSignal(t, r, ctx, f.Home(), "state="+responseState)
				if err := r.S.Navigate(f.Home()+currentPage.path, 15*time.Second); err != nil {
					t.Fatal(err)
				}
				if err := r.check(ctx, "普通流程"); !errors.Is(err, currentPage.want) {
					t.Fatalf("旧接口信号遮住了当前页面拒绝：%v", err)
				}
				if err := r.acquire(ctx, quota.Search, "访问前检查"); !errors.Is(err, currentPage.want) {
					t.Fatalf("访问前也必须检查页面上的拒绝：%v", err)
				}
				if len(budget.got) != 0 {
					t.Fatalf("已拒绝时不能继续申请访问：%v", budget.got)
				}
			})
		}
	}
}
