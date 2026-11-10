package web

import (
	"context"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/pacer"
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
	if boxes, err := r.H.FindNow(ctx, "#alibaba-login-box"); err == nil {
		for _, b := range boxes {
			if r.H.Visible(b) {
				return false, nil
			}
		}
	}
	return r.S.HasCookie([]string{r.Site.Home}, loginCookie)
}

// WaitForLogin 等用户在这个浏览器窗口里自己点登录、扫码（XIANYU.md 8.2 节）。程序不点登录框里的任何东西。
// 每秒在本地检查一次，不访问闲鱼。登录过程中出现的验证码也由用户在这个窗口里处理，所以这里不把验证遮罩当成错误。
func (r *Runner) WaitForLogin(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := r.LoggedIn(ctx)
		if err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrLoginTimeout
		}
		if err := r.P.Wait(ctx, pacer.Between(time.Second, time.Second)); err != nil {
			return err
		}
	}
}
