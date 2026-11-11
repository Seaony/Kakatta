package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/remote"
)

// loginCookie 是表示已登录的 Cookie 名。Mekke 用它判断，上游里没有依据，待用真实页面确认（XIANYU.md 8.3 节）。
const loginCookie = "unb"

// ErrVerificationRefresh 表示页面没有遮罩，但还有未恢复的验证响应。
var ErrVerificationRefresh = errors.New("页面可能还没刷新，请在画面里刷新一下页面，再点我已完成验证")

// OpenLogin 只打开一次首页，失效和验证码留给用户，拒绝或环境异常立即结束。
func (r *Runner) OpenLogin(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := r.CheckVerification(ctx); err != nil {
		return err
	}
	present, err := r.HasLoginCookieContext(ctx)
	if err != nil {
		return err
	}
	r.observation("login_cookie", map[string]any{"before_login_present": present})
	if r.Budget != nil {
		if err := r.Budget.Acquire(quota.Search, "登录首页"); err != nil {
			return err
		}
	}
	start := time.Now()
	err = r.watchChecked(ctx, r.CheckVerification, func(navCtx context.Context) error {
		_, err := r.S.NavigateContext(navCtx, r.Site.Home, rules.timeout("home"))
		return err
	})
	if err != nil {
		return fmt.Errorf("打开登录首页：%w", err)
	}
	r.observeLoginEntry(ctx)
	r.Log("登录首页已打开", map[string]any{"耗时毫秒": time.Since(start).Milliseconds()})
	return nil
}

// ReopenVerification 等待恢复网址时也检查已经到达的拒绝信号。
func (r *Runner) ReopenVerification(ctx context.Context, target string) (bool, error) {
	var refresh bool
	err := r.watchChecked(ctx, func(checkCtx context.Context) error {
		var err error
		refresh, err = r.VerificationPageNeedsRefresh(checkCtx)
		return err
	}, func(navCtx context.Context) error {
		_, err := r.S.NavigateContext(navCtx, target, 30*time.Second)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("重新打开验证网址：%w", err)
	}
	return refresh, nil
}

// BeginVerification 只确认用户已接手的具体响应，在途请求后来返回的拒绝不能被跳过。
func (r *Runner) BeginVerification() {
	signals, _, _ := r.loginSignals()
	var verification []loginSignal
	for _, signal := range signals {
		if !signal.expired {
			verification = append(verification, signal)
		}
	}
	r.handleSignals(verification)
}

// ConfirmVerification 不重新访问页面，用户接手后出现的新风控仍然要拦住。
func (r *Runner) ConfirmVerification(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	pageErr := r.checkPage(ctx, "人工验证完成")
	signals, success, responseErr := r.loginSignals()
	if errors.Is(responseErr, ErrDenied) {
		return responseErr
	}
	if pageErr != nil {
		return pageErr
	}
	if responseErr != nil {
		return responseErr
	}
	var recovered []loginSignal
	var pending error
	for _, signal := range signals {
		handled := r.handledRisks[signal.seq].body
		if signal.header {
			handled = r.handledRisks[signal.seq].header
		}
		if signal.order > 0 && signal.order <= handled {
			continue
		}
		if signal.expired {
			continue
		}
		// 只确认成功接口之前已经到达的头或正文；先发出但晚完成的风险仍然拦住。
		if signal.order > 0 && signal.order < success {
			recovered = append(recovered, signal)
		} else {
			pending = &RiskError{Kind: ErrNeedVerify, Where: "人工验证完成", Signal: signal.signal}
		}
	}
	if pending != nil {
		return fmt.Errorf("%w；%w", pending, ErrVerificationRefresh)
	}
	r.handleSignals(recovered)
	return nil
}

// CheckVerification 检查人工窗口是否还能继续；验证码和待登录留给用户，直接拒绝不能继续。
func (r *Runner) CheckVerification(ctx context.Context) error {
	_, err := r.VerificationPageNeedsRefresh(ctx)
	return err
}

// VerificationPageNeedsRefresh 检查当前画面是否缺少验证框，不发出访问请求。
func (r *Runner) VerificationPageNeedsRefresh(ctx context.Context) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	_, _, responseErr := r.loginSignals()
	if errors.Is(responseErr, ErrDenied) {
		return false, responseErr
	}
	pageErr := r.checkPageState(ctx, "检查人工验证窗口", true)
	if errors.Is(pageErr, ErrDiagnostics) || pageErr != nil && !errors.Is(pageErr, ErrNeedVerify) && !errors.Is(pageErr, ErrLoginExpired) {
		return false, pageErr
	}
	return pageErr == nil && responseErr == nil, responseErr
}

// ErrLoginTimeout 表示在规定时间内用户没有完成登录。
var ErrLoginTimeout = errors.New("等待登录超时")

// LoggedIn 只在本地检查登录状态，不访问闲鱼：主框架不在登录页、没有可见的登录框和验证遮罩、登录 Cookie 存在（XIANYU.md 8.3 节）。
// 只返回是否存在非空登录 Cookie，不返回或保存值。
func (r *Runner) LoggedIn(ctx context.Context) (bool, error) {
	loggedIn, _, err := r.loginState(ctx)
	return loggedIn, err
}

func (r *Runner) loginState(ctx context.Context) (bool, bool, error) {
	if err := contextError(ctx); err != nil {
		return false, false, err
	}
	boxes, err := r.H.FindNow(ctx, rules.Selectors.LoginBox)
	if err != nil {
		return false, false, err
	}
	visibleBox := false
	for _, box := range boxes {
		visible, err := r.H.VisibleCheckedContext(ctx, box)
		if err != nil {
			return false, false, err
		}
		visibleBox = visibleBox || visible
	}
	if err := r.checkPageState(ctx, "检查登录", true); err != nil {
		if errors.Is(err, ErrDiagnostics) {
			return false, visibleBox, err
		}
		if errors.Is(err, ErrLoginExpired) {
			return false, visibleBox, nil
		}
		return false, visibleBox, err
	}
	r.observation("login_selectors", map[string]any{"selector": rules.Selectors.LoginBox, "matched_count": len(boxes), "visible": visibleBox})
	if visibleBox {
		if current, err := r.S.URLContext(ctx); err == nil {
			r.observeIframes(ctx, current, true)
		}
		return false, true, nil
	}
	present, err := r.S.LoginCookiePresentContext(ctx, []string{r.Site.Home}, loginCookie)
	return present, false, err
}

// HasLoginCookie 只查本地有没有这个名字，不读取 Cookie 的值。
func (r *Runner) HasLoginCookie() (bool, error) {
	return r.HasLoginCookieContext(r.S.Context())
}

func (r *Runner) HasLoginCookieContext(ctx context.Context) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	return r.S.HasCookieContext(ctx, []string{r.Site.Home}, loginCookie)
}

// WaitForLogin 等用户在这个浏览器窗口里自己点登录、扫码（XIANYU.md 8.2 节）。程序不点登录框里的任何东西。
// 每秒在本地检查一次，不访问闲鱼。登录过程中出现的验证码也由用户在这个窗口里处理，所以这里不把验证遮罩当成错误。
func (r *Runner) WaitForLogin(ctx context.Context, timeout time.Duration) error {
	return r.WaitForLoginWithViewport(ctx, timeout, nil)
}

// WaitForLoginWithViewport 在原登录检查中附带发布可见验证框的位置。
func (r *Runner) WaitForLoginWithViewport(ctx context.Context, timeout time.Duration, publish func(*remote.Viewport)) error {
	deadline := time.Now().Add(timeout)
	var latestExpired, latestSuccess uint64
	sawLoginBox := false
	var boxNavMark, boxRequestMark uint64
	for {
		if err := contextError(ctx); err != nil {
			return err
		}
		signals, success, responseErr := r.loginSignals()
		if responseErr != nil {
			return responseErr
		}
		latestSuccess = max(latestSuccess, success)
		for _, signal := range signals {
			if signal.expired {
				latestExpired = max(latestExpired, signal.order)
			}
		}
		ok, visibleBox, err := r.loginState(ctx)
		if publish != nil && (err == nil || errors.Is(err, ErrNeedVerify)) {
			var view *remote.Viewport
			if errors.Is(err, ErrNeedVerify) {
				view, _ = r.VerificationViewport(ctx)
			}
			publish(view)
		}
		if visibleBox && !sawLoginBox {
			sawLoginBox = true
			boxNavMark, boxRequestMark = r.S.NavigationMark(), r.S.Capture.Mark()
			r.Log("登录框已出现", nil)
		}
		recovered := latestExpired == 0 || latestSuccess > latestExpired || sawLoginBox && !visibleBox
		if err == nil && ok && recovered {
			r.handleSignals(signals)
			present, cookieErr := r.HasLoginCookieContext(ctx)
			if cookieErr != nil {
				return cookieErr
			}
			r.observation("login_cookie", map[string]any{"after_login_present": present, "logout_checked": false})
			if sawLoginBox {
				r.observation("login_refresh", map[string]any{"after_login_box_seen": true, "main_frame_navigation": r.S.NavigationMark() > boxNavMark, "mtop_sent": r.S.Capture.SentAfter(boxRequestMark, r.Site.mtop()), "mtop_completed": len(r.S.Capture.DoneAfter(boxRequestMark, r.Site.mtop())), "login_box_disappeared": !visibleBox})
			}
			return nil
		}
		// 验证码留给用户在窗口里处理；环境异常和直接拒绝用户处理不了，立即结束。
		if err != nil && (errors.Is(err, ErrDiagnostics) || !errors.Is(err, ErrNeedVerify)) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLoginTimeout
		}
		if err := r.P.Wait(ctx, pacer.Between(rules.timeout("login_poll"), rules.timeout("login_poll"))); err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return ctxErr
			}
			return err
		}
	}
}

// RequireLoginCookie 做本地预检。这个名字还没在真实页面确认，缺失只记警告，不判登录失效。
func (r *Runner) RequireLoginCookie() error {
	return r.RequireLoginCookieContext(r.S.Context())
}

func (r *Runner) RequireLoginCookieContext(ctx context.Context) error {
	ok, err := r.HasLoginCookieContext(ctx)
	if err != nil {
		return fmt.Errorf("读取浏览器登录状态：%w", err)
	}
	r.observation("login_cookie", map[string]any{"before_navigation_present": ok})
	if !ok {
		r.Log("本地登录预检警告", map[string]any{"原因": "没有 unb Cookie，是否已失效以首页登录状态和本次目标接口为准"})
	}
	return nil
}

// RequireLoggedIn 在已经打开的闲鱼页面上确认登录还在。
func (r *Runner) RequireLoggedIn(ctx context.Context, where string) error {
	ok, err := r.LoggedIn(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s：尚未确认登录完成", where)
	}
	return nil
}

// CheckLogin 只打开首页确认登录还在，算一次搜索类访问。用来测登录状态能保持多久。
func (r *Runner) CheckLogin(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := r.RequireLoginCookieContext(ctx); err != nil {
		return err
	}
	if err := r.acquire(ctx, quota.Search, "检查登录时打开首页"); err != nil {
		return err
	}
	if err := r.navigate(ctx, r.Site.Home, "检查登录", rules.timeout("home")); err != nil {
		if cerr := r.check(ctx, "检查登录"); cerr != nil {
			return cerr
		}
		return fmt.Errorf("打开首页：%w", err)
	}
	if err := r.wait(ctx, "首页停留", r.Pace.HomeStay); err != nil {
		return err
	}
	if err := r.check(ctx, "检查登录"); err != nil {
		return err
	}
	return nil
}
