package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Seaony/Kakatta/internal/browser"
)

func TestVisibleNicknameRecognizesExistingSessionWithoutCookie(t *testing.T) {
	r, ctx := loginEvidenceRunner(t, "fixture/login-header?nick=valid")
	if present, err := r.HasLoginCookieContext(ctx); err != nil || present {
		t.Fatalf("正面结构夹具不能预写登录标记：存在%v，错误%v", present, err)
	}
	confirmed := false
	r.Log = func(event string, fields map[string]any) {
		encoded, _ := json.Marshal(fields)
		if strings.Contains(string(encoded), "本地测试号") {
			t.Error("用户区诊断不能输出昵称原文")
		}
		if event != "待确认观察" || fields["事项"] != "login_header" {
			return
		}
		results, _ := fields["结果"].(map[string]any)
		for _, raw := range results {
			stage, _ := raw.(map[string]any)
			confirmed = confirmed || stage["identity_confirmed"] == true
		}
	}
	if ok, err := r.LoggedIn(ctx); err != nil || !ok {
		t.Errorf("可见且非登录文案的真实昵称节点应确认已有会话：已登录%v，错误%v", ok, err)
	}
	mark := r.S.Capture.Mark()
	limited, cancel := context.WithTimeout(ctx, time.Second)
	err := r.WaitForLogin(limited, time.Second)
	cancel()
	if err != nil {
		t.Errorf("已有正面身份不能要求重复扫码：%v", err)
	}
	if !confirmed {
		t.Error("正式用户区观察没有确认同一正面节点")
	}
	if r.S.Capture.Mark() != mark {
		t.Error("检查已有登录不应额外导航或请求接口")
	}
}

func TestInvalidNicknameCannotRecognizeExistingSession(t *testing.T) {
	for _, state := range []string{"empty", "login", "hidden", "avatar", "absent"} {
		t.Run(state, func(t *testing.T) {
			r, ctx := loginEvidenceRunner(t, "fixture/login-header?nick="+state)
			if ok, err := r.LoggedIn(ctx); err != nil || ok {
				t.Fatalf("缺少可见非空且非登录文案的昵称不能当成功：已登录%v，错误%v", ok, err)
			}
			limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			err := r.WaitForLogin(limited, time.Second)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("无效昵称只能继续等待：%v", err)
			}
		})
	}
}

func TestFreshScanStillNeedsPositiveNickname(t *testing.T) {
	for _, state := range []string{"empty", "login", "hidden", "avatar", "absent"} {
		t.Run(state, func(t *testing.T) {
			r, ctx := loginEvidenceRunner(t, "fixture/login-delayed?completion=mtop&nick="+state)
			result, observed := waitForLoginEvidence(t, r, ctx)
			requireLoginBoxObserved(t, result, observed)
			clickLoginEvidenceButton(t, r, ctx, "#finish-login")
			before := r.S.Capture.Mark()
			clickLoginEvidenceButton(t, r, ctx, "#continue-login")
			if _, err := r.S.Capture.WaitAfter(ctx, before, browser.Match{URL: "after_scan=1"}, time.Second); err != nil {
				t.Fatal("夹具没有实际产生本次新接口证据", err)
			}
			requireLoginStillPending(t, result, 250*time.Millisecond)
		})
	}
}

func TestPositiveNicknameCannotReplaceFreshActivityAfterBox(t *testing.T) {
	r, ctx := loginEvidenceRunner(t, "fixture/login-delayed?completion=none&nick=valid")
	result, observed := waitForLoginEvidence(t, r, ctx)
	requireLoginBoxObserved(t, result, observed)
	clickLoginEvidenceButton(t, r, ctx, "#finish-login")
	beforeNav, beforeRequest := r.S.NavigationMark(), r.S.Capture.Mark()
	clickLoginEvidenceButton(t, r, ctx, "#continue-login")
	requireLoginStillPending(t, result, 250*time.Millisecond)
	if r.S.NavigationMark() != beforeNav || r.S.Capture.Mark() != beforeRequest {
		t.Fatal("纯关框加昵称变化的夹具不能产生导航或接口证据")
	}
}

func TestExistingNicknameDoesNotOverrideUnresolvedRisk(t *testing.T) {
	for _, risk := range []struct {
		state string
		want  error
	}{{"verify", ErrNeedVerify}, {"denied", ErrDenied}, {"mask", ErrNeedVerify}, {"environment", ErrEnvBlocked}} {
		t.Run(risk.state, func(t *testing.T) {
			r, ctx := loginEvidenceRunner(t, "fixture/login-header?nick=valid")
			switch risk.state {
			case "verify", "denied":
				sendLoginSignal(t, r, ctx, r.Site.Home[:strings.Index(r.Site.Home, "fixture/")], "state="+risk.state)
			case "mask":
				if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](`document.body.insertAdjacentHTML('beforeend', '<div class="baxia-dialog-mask" style="width:200px;height:200px">验证</div>'); true`)); err != nil {
					t.Fatal(err)
				}
			case "environment":
				if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](`document.body.insertAdjacentHTML('beforeend', '<div>非法访问</div>'); true`)); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := r.LoggedIn(ctx); ok || !errors.Is(err, risk.want) {
				t.Errorf("正面昵称不能覆盖未处理风险：已登录%v，错误%v", ok, err)
			}
			limited, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			err := r.WaitForLogin(limited, time.Second)
			cancel()
			want := risk.want
			if want == ErrNeedVerify {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Errorf("验证仍需等待、拒绝与环境异常立即结束：%v", err)
			}
			if err := r.check(ctx, "确认旧风险仍存在"); !errors.Is(err, risk.want) {
				t.Fatalf("已有登录分支不能确认或丢弃旧风险：%v", err)
			}
		})
	}
}

func TestExistingNicknameOnlyObservesSideSessionExpiry(t *testing.T) {
	r, ctx := loginEvidenceRunner(t, "fixture/login-header?nick=valid")
	home := r.Site.Home[:strings.Index(r.Site.Home, "fixture/")]
	sendLoginSignal(t, r, ctx, home, "state=expired")
	if ok, err := r.LoggedIn(ctx); err != nil || !ok {
		t.Fatalf("旁路失效不能否定正面身份：已登录%v，错误%v", ok, err)
	}
	limited, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := r.WaitForLogin(limited, time.Second); err != nil {
		t.Fatal("旁路失效应只记录观察", err)
	}
}

func TestExistingNicknameKeepsLateRiskBody(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home()+"fixture/login-header?nick=valid", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	mark := startLoginSignal(t, r, f.Home(), "state=denied&hold=1")
	if ok, err := r.LoggedIn(ctx); err != nil || !ok {
		t.Fatalf("响应未到时夹具应有正面身份：已登录%v，错误%v", ok, err)
	}
	f.ReleaseLoginResponses()
	if _, err := r.S.Capture.WaitAfter(ctx, mark, browser.Match{URL: "state=denied&hold=1"}, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.check(ctx, "已有会话随后收到拒绝"); !errors.Is(err, ErrDenied) {
		t.Fatalf("登录判断不能跳过先前请求后来才到的拒绝：%v", err)
	}
}

func TestNicknameOutsideConfirmedUserAreaCannotAuthenticate(t *testing.T) {
	r, ctx := loginEvidenceRunner(t, "fixture/login-header?nick=login")
	if _, err := chromedp.Run(r.S.Context(), chromedp.Evaluate[bool](`document.body.insertAdjacentHTML('beforeend', '<div class="nick--outside">本地外部示例</div>'); true`)); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.LoggedIn(ctx); err != nil || ok {
		t.Fatalf("用户区之外相似节点不能作为身份：已登录%v，错误%v", ok, err)
	}
}
