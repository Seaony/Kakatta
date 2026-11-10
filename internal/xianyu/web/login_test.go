package web

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func TestConfirmVerificationUsesCurrentPageAndClearsHandledBoundary(t *testing.T) {
	f := newFake(t)
	f.SetDetailRet("RGV587::需要验证")
	r, _, ctx := startRunner(t, f)
	if _, err := r.OpenDetail(ctx, xianyu.WebLink("fleamarket://item?id=101")); !errors.Is(err, ErrNeedVerify) {
		t.Fatalf("应先出现接口风控：%v", err)
	}
	r.BeginVerification()
	if err := r.checkResponses("用户接手"); err != nil {
		t.Fatalf("用户接手后不重放上次运行的风控：%v", err)
	}
	for _, tc := range []struct {
		path string
		want error
	}{{"punish?action=captcha", ErrNeedVerify}, {"mini_login", ErrLoginExpired}} {
		if err := r.S.Navigate(f.Home()+tc.path, 15*time.Second); err != nil {
			t.Fatal(err)
		}
		boundary := r.since
		if err := r.ConfirmVerification(ctx); !errors.Is(err, tc.want) {
			t.Fatalf("当前页面未恢复，必须保留原错误：%v", err)
		}
		if r.since != boundary {
			t.Fatal("失败确认不能推进风险边界")
		}
	}
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	before := r.S.Capture.Mark()
	if err := r.ConfirmVerification(ctx); err != nil {
		t.Fatal(err)
	}
	if after := r.S.Capture.Mark(); after != before {
		t.Fatalf("确认只能检查本地页面，不能新增访问：%d→%d", before, after)
	}
	if head, err := r.OwnProfile(ctx); err != nil || head.Nick != "测试号" {
		t.Fatalf("完成后不能重放旧风控：%+v %v", head, err)
	}
}

func TestConfirmVerificationRejectsNewInterfaceRiskAfterUserTakesOver(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.OpenHome(ctx); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	boundary := r.since
	mark := r.S.Capture.Mark()
	js := `fetch('` + f.Home() + `h5/mtop.fake.risk/1.0/'); true`
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](js)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "/h5/mtop.fake.risk/"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	before := r.S.Capture.Mark()
	if err := r.CheckVerification(ctx); err != nil {
		t.Fatalf("新的验证码仍留在人工窗口处理：%v", err)
	}
	for range 2 {
		if err := r.ConfirmVerification(ctx); !errors.Is(err, ErrNeedVerify) || !strings.Contains(err.Error(), "页面可能还没刷新，请在画面里刷新一下页面，再点我已完成验证") {
			t.Fatalf("人工阶段的新接口风控应保留，并提示在画面里刷新：%v", err)
		}
		if r.since != boundary {
			t.Fatal("失败确认不能忽略新的接口风控")
		}
	}
	if after := r.S.Capture.Mark(); after != before {
		t.Fatalf("确认不能产生额外访问：%d→%d", before, after)
	}
}

func TestConfirmVerificationRecoversCompletedRiskAfterSuccess(t *testing.T) {
	for _, successStartedFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(successStartedFirst), func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			r.BeginVerification()
			var mark uint64
			if successStartedFirst {
				mark = startLoginSignal(t, r, f.Home(), "state=success&hold=1")
			}
			sendLoginSignal(t, r, ctx, f.Home(), "state=verify&punish_header=1")
			if successStartedFirst {
				f.ReleaseLoginResponses()
				if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "state=success"}, 5*time.Second); err != nil {
					t.Fatal(err)
				}
			} else {
				otherLoginSuccess(t, r, ctx, f.Home())
			}
			before := r.S.Capture.Mark()
			if err := r.ConfirmVerification(ctx); err != nil {
				t.Fatalf("已完成验证码之后收到成功响应，当前页面正常，应允许确认：%v", err)
			}
			if err := r.checkResponses("确认完成后"); err != nil {
				t.Fatalf("已经确认的头和正文不应重放：%v", err)
			}
			if after := r.S.Capture.Mark(); after != before {
				t.Fatalf("确认不能产生新请求：%d→%d", before, after)
			}
		})
	}
}

func TestConfirmVerificationSuccessCannotClearDenialOrExpired(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  error
	}{{"denied", ErrDenied}, {"expired", ErrLoginExpired}} {
		t.Run(tc.state, func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			sendLoginSignal(t, r, ctx, f.Home(), "state="+tc.state)
			r.BeginVerification()
			otherLoginSuccess(t, r, ctx, f.Home())
			before := r.S.Capture.Mark()
			if err := r.ConfirmVerification(ctx); !errors.Is(err, tc.want) {
				t.Fatalf("人工验证不能通过后续成功清除%s：%v", tc.state, err)
			}
			if after := r.S.Capture.Mark(); after != before {
				t.Fatalf("确认不能产生新请求：%d→%d", before, after)
			}
		})
	}
}

func TestConfirmVerificationKeepsRiskCompletedAfterSuccess(t *testing.T) {
	for _, header := range []bool{false, true} {
		t.Run(strconv.FormatBool(header), func(t *testing.T) {
			f := newFake(t)
			r, _, ctx := startRunner(t, f)
			if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
				t.Fatal(err)
			}
			r.BeginVerification()
			query := "state=verify&hold=1"
			if header {
				query += "&punish_header=1"
			}
			mark := startLoginSignal(t, r, f.Home(), query)
			if header {
				deadline := time.Now().Add(5 * time.Second)
				for len(r.S.Capture.HeadersAfter(mark, browser.Match{URL: "state=verify"})) == 0 {
					if time.Now().After(deadline) {
						t.Fatal("没有收到处罚头")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			otherLoginSuccess(t, r, ctx, f.Home())
			f.ReleaseLoginResponses()
			response, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "state=verify"}, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			before := r.S.Capture.Mark()
			if err := r.ConfirmVerification(ctx); !errors.Is(err, ErrNeedVerify) {
				t.Fatalf("旧请求在成功之后才完成验证响应，仍须拦住：%v", err)
			}
			if r.handledRisks[response.Seq].body >= response.BodyOrder {
				t.Fatal("不能因头部较早到达而确认较晚正文")
			}
			if after := r.S.Capture.Mark(); after != before {
				t.Fatalf("确认不能产生新请求：%d→%d", before, after)
			}
			otherLoginSuccess(t, r, ctx, f.Home())
			if err := r.ConfirmVerification(ctx); err != nil {
				t.Fatalf("较晚验证也有后续成功后，应可确认：%v", err)
			}
		})
	}
}

func TestConfirmVerificationEnvironmentBlockWinsOverInterfaceVerification(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	r.BeginVerification()
	sendLoginSignal(t, r, ctx, f.Home(), "state=verify")
	if err := r.S.Navigate(f.Home()+"blocked-mask", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmVerification(ctx); !errors.Is(err, ErrEnvBlocked) {
		t.Fatalf("可见环境异常不能被接口验证码遮住：%v", err)
	}
}
