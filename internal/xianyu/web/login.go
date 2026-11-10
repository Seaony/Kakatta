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

// ErrLoginTimeout 表示在规定时间内用户没有完成登录。
var ErrLoginTimeout = errors.New("等待登录超时")

// LoggedIn 只在本地检查登录状态，不访问闲鱼：主框架不在登录页、没有可见的登录框和验证遮罩、登录 Cookie 存在（XIANYU.md 8.3 节）。
// 只判断 Cookie 在不在，不读取它的值。
func (r *Runner) LoggedIn(ctx context.Context) (bool, error) {
	if err := r.checkPage(ctx, "检查登录"); err != nil {
		if errors.Is(err, ErrLoginExpired) {
			return false, nil
		}
		return false, err
	}
	boxes, err := r.H.FindNow(ctx, "#alibaba-login-box")
	if err != nil {
		return false, err
	}
	for _, b := range boxes {
		visible, err := r.H.VisibleChecked(b)
		if err != nil {
			return false, err
		}
		if visible {
			return false, nil
		}
	}
	return r.S.LoginCookiePresent([]string{r.Site.Home}, loginCookie)
}

// HasLoginCookie 只查本地有没有这个名字，不读取 Cookie 的值。
func (r *Runner) HasLoginCookie() (bool, error) {
	return r.S.HasCookie([]string{r.Site.Home}, loginCookie)
}

// WaitForLogin 等用户在这个浏览器窗口里自己点登录、扫码（XIANYU.md 8.2 节）。程序不点登录框里的任何东西。
// 每秒在本地检查一次，不访问闲鱼。登录过程中出现的验证码也由用户在这个窗口里处理，所以这里不把验证遮罩当成错误。
func (r *Runner) WaitForLogin(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := r.LoggedIn(ctx)
		if err == nil && ok {
			// 人工登录可能已经处理过验证，后续取昵称不再重放处理前的风控。
			r.since = r.S.Capture.Mark()
			return nil
		}
		// 验证码留给用户在窗口里处理；环境异常和直接拒绝用户处理不了，立即结束。
		if err != nil && !errors.Is(err, ErrNeedVerify) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLoginTimeout
		}
		if err := r.P.Wait(ctx, pacer.Between(time.Second, time.Second)); err != nil {
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
	if err := r.S.Navigate(r.Site.Home, 30*time.Second); err != nil {
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
