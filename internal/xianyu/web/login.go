package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
)

// loginCookie 是表示已登录的 Cookie 名。Mekke 用它判断，上游里没有依据，待用真实页面确认（XIANYU.md 8.3 节）。
const loginCookie = "unb"

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
	if err := ctx.Err(); err != nil {
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
			return &RiskError{Kind: ErrLoginExpired, Where: "人工验证完成", Signal: signal.signal}
		}
		// 只确认成功接口之前已经到达的头或正文；先发出但晚完成的风险仍然拦住。
		if signal.order > 0 && signal.order < success {
			recovered = append(recovered, signal)
		} else {
			pending = &RiskError{Kind: ErrNeedVerify, Where: "人工验证完成", Signal: signal.signal}
		}
	}
	if pending != nil {
		return fmt.Errorf("%w；页面可能还没刷新，请在画面里刷新一下页面，再点我已完成验证", pending)
	}
	r.handleSignals(recovered)
	return nil
}

// CheckVerification 检查人工窗口是否还能继续；验证码和待登录留给用户，直接拒绝不能继续。
func (r *Runner) CheckVerification(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, responseErr := r.loginSignals()
	if errors.Is(responseErr, ErrDenied) {
		return responseErr
	}
	pageErr := r.checkPage(ctx, "检查人工验证窗口")
	if pageErr != nil && !errors.Is(pageErr, ErrNeedVerify) && !errors.Is(pageErr, ErrLoginExpired) {
		return pageErr
	}
	return responseErr
}

// ErrLoginTimeout 表示在规定时间内用户没有完成登录。
var ErrLoginTimeout = errors.New("等待登录超时")

// LoggedIn 只在本地检查登录状态，不访问闲鱼：主框架不在登录页、没有可见的登录框和验证遮罩、登录 Cookie 存在（XIANYU.md 8.3 节）。
// 只判断 Cookie 在不在，不读取它的值。
func (r *Runner) LoggedIn(ctx context.Context) (bool, error) {
	loggedIn, _, err := r.loginState(ctx)
	return loggedIn, err
}

func (r *Runner) loginState(ctx context.Context) (bool, bool, error) {
	boxes, err := r.H.FindNow(ctx, rules.Selectors.LoginBox)
	if err != nil {
		return false, false, err
	}
	visibleBox := false
	for _, box := range boxes {
		visible, err := r.H.VisibleChecked(box)
		if err != nil {
			return false, false, err
		}
		visibleBox = visibleBox || visible
	}
	if err := r.checkPage(ctx, "检查登录"); err != nil {
		if errors.Is(err, ErrLoginExpired) {
			return false, visibleBox, nil
		}
		return false, visibleBox, err
	}
	if visibleBox {
		return false, true, nil
	}
	present, err := r.S.LoginCookiePresent([]string{r.Site.Home}, loginCookie)
	return present, false, err
}

// HasLoginCookie 只查本地有没有这个名字，不读取 Cookie 的值。
func (r *Runner) HasLoginCookie() (bool, error) {
	return r.S.HasCookie([]string{r.Site.Home}, loginCookie)
}

// WaitForLogin 等用户在这个浏览器窗口里自己点登录、扫码（XIANYU.md 8.2 节）。程序不点登录框里的任何东西。
// 每秒在本地检查一次，不访问闲鱼。登录过程中出现的验证码也由用户在这个窗口里处理，所以这里不把验证遮罩当成错误。
func (r *Runner) WaitForLogin(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var latestExpired, latestSuccess uint64
	sawLoginBox := false
	for {
		if err := ctx.Err(); err != nil {
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
		if visibleBox && !sawLoginBox {
			sawLoginBox = true
			r.Log("登录框已出现", nil)
		}
		recovered := latestExpired == 0 || latestSuccess > latestExpired || sawLoginBox && !visibleBox
		if err == nil && ok && recovered {
			r.handleSignals(signals)
			return nil
		}
		// 验证码留给用户在窗口里处理；环境异常和直接拒绝用户处理不了，立即结束。
		if err != nil && !errors.Is(err, ErrNeedVerify) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLoginTimeout
		}
		if err := r.P.Wait(ctx, pacer.Between(rules.timeout("login_poll"), rules.timeout("login_poll"))); err != nil {
			return err
		}
	}
}

// RequireLoginCookie 做本地预检。这个名字还没在真实页面确认，缺失只记警告，不判登录失效。
func (r *Runner) RequireLoginCookie() error {
	ok, err := r.HasLoginCookie()
	if err != nil {
		return fmt.Errorf("读取浏览器登录状态：%w", err)
	}
	if !ok {
		r.Log("本地登录预检警告", map[string]any{"原因": "没有 unb Cookie，是否已失效以首页跳转为准"})
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
	if err := r.RequireLoginCookie(); err != nil {
		return err
	}
	if err := r.acquire(quota.Search, "检查登录时打开首页"); err != nil {
		return err
	}
	if err := r.S.Navigate(r.Site.Home, rules.timeout("home")); err != nil {
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
