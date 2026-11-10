// Package web 在闲鱼网页上执行具体操作：打开首页、在搜索框里搜索、选筛选、打开详情、等用户登录，并在每一步检查风控。
// 页面、接口和选择器以 docs/XIANYU.md 为准；节奏和额度以 docs/CRAWL-POLICY.md 为准。
package web

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

// 风控类错误。出现任何一种都要立即结束整次运行，不能被普通错误处理吞掉（CRAWL-POLICY 第四节）。
var (
	ErrNeedVerify   = errors.New("闲鱼要求人机验证")
	ErrDenied       = errors.New("闲鱼直接拒绝访问")
	ErrEnvBlocked   = errors.New("闲鱼判定浏览器环境异常")
	ErrLoginExpired = errors.New("闲鱼登录已失效")
)

// RiskError 记录风控出现在哪一步、凭什么判断的。
type RiskError struct {
	Kind   error  // 上面四种之一
	Where  string // 哪一步
	Signal string // 哪个信号，比如 "ret:FAIL_SYS_USER_VALIDATE"
}

func (e *RiskError) Error() string {
	return fmt.Sprintf("%s（%s，信号：%s）", e.Kind, e.Where, e.Signal)
}

func (e *RiskError) Unwrap() error { return e.Kind }

// IsStop 判断错误是不是必须立即结束整次运行的那一类：人机验证、拒绝、环境异常、登录失效。
func IsStop(err error) bool {
	return errors.Is(err, ErrNeedVerify) || errors.Is(err, ErrDenied) ||
		errors.Is(err, ErrEnvBlocked) || errors.Is(err, ErrLoginExpired)
}

// 页面上表示需要验证的遮罩（scraper.py:740-741）。
var verifyMasks = []string{"div.baxia-dialog-mask", "div.J_MIDDLEWARE_FRAME_WIDGET"}

// 页面上表示环境被识别的文字（#339）。
var blockedTexts = []string{"非法访问", "请使用正常浏览器访问"}

// checkPage 检查当前页面：网址是不是登录页、有没有可见的验证遮罩、有没有「非法访问」文字。
func (r *Runner) checkPage(ctx context.Context, where string) error {
	url, err := r.S.URL()
	if err == nil {
		low := strings.ToLower(url)
		if strings.Contains(low, "passport.goofish.com") || strings.Contains(low, "mini_login") {
			return &RiskError{Kind: ErrLoginExpired, Where: where, Signal: "跳到了登录页"}
		}
	}
	for _, sel := range verifyMasks {
		nodes, err := r.H.FindNow(ctx, sel)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			// 每个正常页面都预加载 baxia 脚本，只认真正显示出来的遮罩。
			if r.H.Visible(n) {
				return &RiskError{Kind: ErrNeedVerify, Where: where, Signal: "遮罩 " + sel}
			}
		}
	}
	for _, text := range blockedTexts {
		nodes, err := r.H.FindNow(ctx, "//*[contains(text(),'"+text+"')]")
		if err != nil {
			continue
		}
		for _, n := range nodes {
			if r.H.Visible(n) {
				return &RiskError{Kind: ErrEnvBlocked, Where: where, Signal: "页面文字「" + text + "」"}
			}
		}
	}
	return nil
}

// checkResponses 检查 mark 之后完成的全部 mtop 接口：响应头 bxpunish、ret 和 data.url。
// 不管是不是正在等的那个接口，只要是风控就立即结束（XIANYU.md 4.1 节）。
func (r *Runner) checkResponses(mark uint64, where string) error {
	for _, resp := range r.S.Capture.DoneAfter(mark, r.Site.mtop()) {
		if resp.Headers["bxpunish"] == "1" {
			return &RiskError{Kind: ErrNeedVerify, Where: where, Signal: "响应头 bxpunish"}
		}
		if resp.Err != nil || len(resp.Body) == 0 {
			continue
		}
		kind, ret, err := xianyu.CheckEnvelope(resp.Body)
		if err != nil {
			continue
		}
		switch kind {
		case xianyu.RetNeedVerify:
			return &RiskError{Kind: ErrNeedVerify, Where: where, Signal: "ret:" + strings.Join(ret, ",")}
		case xianyu.RetDenied:
			return &RiskError{Kind: ErrDenied, Where: where, Signal: "ret:" + strings.Join(ret, ",")}
		case xianyu.RetSessionExpired:
			return &RiskError{Kind: ErrLoginExpired, Where: where, Signal: "ret:" + strings.Join(ret, ",")}
		}
	}
	return nil
}

// check 做一次完整的风控检查：先查接口，再查页面。
func (r *Runner) check(ctx context.Context, mark uint64, where string) error {
	if err := r.checkResponses(mark, where); err != nil {
		return err
	}
	return r.checkPage(ctx, where)
}

// waitAPI 等 mark 之后的第一条有效接口响应：令牌过期的那条跳过、继续等；风控立即返回；业务失败算这一步失败。
func (r *Runner) waitAPI(ctx context.Context, mark uint64, m browser.Match, where string, timeoutSec int) (*browser.Response, error) {
	after := mark
	for {
		resp, err := r.S.Capture.WaitAfter(ctx, after, m, seconds(timeoutSec))
		if err != nil {
			// 超时先看是不是掉了登录或出了验证（scraper.py:728-733）。
			if cerr := r.check(ctx, mark, where); cerr != nil {
				return nil, cerr
			}
			if r.S.Capture.SentAfter(mark, m) {
				return nil, fmt.Errorf("%s：请求已发出但没有返回：%w", where, err)
			}
			return nil, fmt.Errorf("%s：没有发出请求：%w", where, err)
		}
		if cerr := r.check(ctx, mark, where); cerr != nil {
			return nil, cerr
		}
		if resp.Err != nil {
			return nil, fmt.Errorf("%s：请求失败：%w", where, resp.Err)
		}
		if resp.Status < 200 || resp.Status > 299 {
			return nil, fmt.Errorf("%s：HTTP 状态码 %d", where, resp.Status)
		}
		kind, ret, err := xianyu.CheckEnvelope(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("%s：响应不是有效的接口数据：%w", where, err)
		}
		switch kind {
		case xianyu.RetOK:
			return resp, nil
		case xianyu.RetTokenExpired:
			// 页面会自己刷新令牌并重发，继续等下一条。
			after = resp.Seq
			continue
		default:
			return nil, fmt.Errorf("%s：接口业务失败：%s", where, strings.Join(ret, ","))
		}
	}
}
