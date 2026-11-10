// Package web 在闲鱼网页上执行具体操作：打开首页、在搜索框里搜索、选筛选、打开详情、等用户登录，并在每一步检查风控。
// 页面、接口和选择器以 docs/XIANYU.md 为准；节奏和额度以 docs/CRAWL-POLICY.md 为准。
package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

// checkPage 检查当前页面：网址是不是登录页、有没有可见的验证遮罩、有没有「非法访问」文字。
func (r *Runner) checkPage(ctx context.Context, where string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	current, err := r.S.URLContext(ctx)
	if err != nil {
		return fmt.Errorf("%s：读取当前网址失败：%w", where, err)
	}
	if current == "about:blank" {
		// 刚启动、还没打开过闲鱼页面，没有可查的；在空白页上查元素会一直等到超时。
		return nil
	}
	var verification error
	if err := pageURLRisk(current, where); err != nil {
		if !errors.Is(err, ErrNeedVerify) {
			return err
		}
		verification = err
	}
	for _, sel := range rules.Selectors.VerifyMasks {
		nodes, err := r.H.FindNow(ctx, sel)
		if err != nil {
			return fmt.Errorf("%s：检查验证遮罩失败：%w", where, err)
		}
		for _, n := range nodes {
			// 每个正常页面都预加载 baxia 脚本，只认真正显示出来的遮罩。
			visible, err := r.H.VisibleCheckedContext(ctx, n)
			if err != nil {
				return fmt.Errorf("%s：检查验证遮罩是否可见失败：%w", where, err)
			}
			if visible {
				verification = &RiskError{Kind: ErrNeedVerify, Where: where, Signal: "遮罩 " + sel}
			}
		}
	}
	for _, text := range rules.Selectors.BlockedTexts {
		nodes, err := r.H.SearchText(ctx, text)
		if err != nil {
			return fmt.Errorf("%s：检查环境提示失败：%w", where, err)
		}
		for _, n := range nodes {
			visible, err := r.H.VisibleCheckedContext(ctx, n)
			if err != nil {
				return fmt.Errorf("%s：检查环境提示是否可见失败：%w", where, err)
			}
			if visible {
				return &RiskError{Kind: ErrEnvBlocked, Where: where, Signal: "页面文字「" + text + "」"}
			}
		}
	}
	return verification
}

func pageURLRisk(current, where string) error {
	low := strings.ToLower(current)
	switch {
	case strings.Contains(low, "passport.goofish.com"), strings.Contains(low, "mini_login"):
		return &RiskError{Kind: ErrLoginExpired, Where: where, Signal: "跳到了登录页"}
	case strings.Contains(low, "punish"), strings.Contains(low, "_____tmd_____"):
		if strings.Contains(low, "action=deny") {
			return &RiskError{Kind: ErrDenied, Where: where, Signal: "主框架拒绝访问"}
		}
		return &RiskError{Kind: ErrNeedVerify, Where: where, Signal: "主框架需要验证"}
	}
	return nil
}

// 接口跳到处罚网址后仍按原接口检查，后台跳转不代表主框架已跳转。
func (r *Runner) isMtopResponse(response *browser.Response) bool {
	fragment := r.Site.mtop().URL
	return strings.Contains(response.RequestURL, fragment) || strings.Contains(response.URL, fragment)
}

// checkResponses 检查这个 Runner 创建以来完成的全部 mtop 接口：响应头 bxpunish、ret 和 data.url。
// 不管是不是正在等的那个接口、是哪一步发出的，只要是风控就立即结束（XIANYU.md 4.1 节）：
// 两步之间才返回的风控响应也要在下一次检查时拦住，所以不按每一步自己的标记来查。
func (r *Runner) checkResponses(where string) error {
	if err := r.S.Capture.Err(); err != nil {
		return fmt.Errorf("%s：接口监听失效：%w", where, err)
	}
	var pending error
	for _, resp := range r.S.Capture.HeadersAfter(r.since, browser.Match{}) {
		if (resp.MainDocument || r.isMtopResponse(resp)) && resp.Headers["bxpunish"] == "1" && (resp.HeaderOrder == 0 || resp.HeaderOrder > r.handledRisks[resp.Seq].header) {
			pending = &RiskError{Kind: ErrNeedVerify, Where: where, Signal: "响应头 bxpunish"}
		}
	}
	for _, resp := range r.S.Capture.DoneAfter(r.since, r.Site.mtop()) {
		if resp.MainDocument || !r.isMtopResponse(resp) {
			continue
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
			if (resp.BodyOrder == 0 || resp.BodyOrder > r.handledRisks[resp.Seq].body) && pending == nil {
				pending = &RiskError{Kind: ErrNeedVerify, Where: where, Signal: "ret:" + strings.Join(ret, ",")}
			}
		case xianyu.RetDenied:
			return &RiskError{Kind: ErrDenied, Where: where, Signal: "ret:" + strings.Join(ret, ",")}
		case xianyu.RetSessionExpired:
			if resp.BodyOrder == 0 || resp.BodyOrder > r.handledRisks[resp.Seq].body {
				pending = &RiskError{Kind: ErrLoginExpired, Where: where, Signal: "ret:" + strings.Join(ret, ",")}
			}
		}
	}
	return pending
}

// check 做一次完整的风控检查：先查接口，再查页面。
func (r *Runner) check(ctx context.Context, where string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	responseErr := r.checkResponses(where)
	if errors.Is(responseErr, ErrDenied) || errors.Is(responseErr, ErrEnvBlocked) {
		return responseErr
	}
	return combinedRisk(responseErr, r.checkPage(ctx, where))
}

func combinedRisk(responseErr, pageErr error) error {
	if errors.Is(responseErr, ErrDenied) || errors.Is(responseErr, ErrEnvBlocked) {
		return responseErr
	}
	if errors.Is(pageErr, ErrDenied) || errors.Is(pageErr, ErrEnvBlocked) {
		return pageErr
	}
	// 已有验证码也要继续检查页面，读取失败不能当成仍可人工处理。
	if pageErr != nil && !IsStop(pageErr) {
		return pageErr
	}
	if responseErr != nil {
		return responseErr
	}
	return pageErr
}

// waitAPI 等 mark 之后的第一条有效接口响应：令牌过期的那条跳过、继续等；风控立即返回；业务失败算这一步失败。
func (r *Runner) waitAPI(ctx context.Context, mark uint64, m browser.Match, where string, timeout time.Duration) (*browser.Response, error) {
	return r.waitAPIResponse(ctx, mark, m, where, timeout, 0)
}

func (r *Runner) waitAPIResponse(ctx context.Context, mark uint64, m browser.Match, where string, timeout, quiet time.Duration) (*browser.Response, error) {
	after := mark
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		resp, err := r.waitResponse(ctx, after, m, where, time.Until(deadline), quiet)
		if err != nil {
			if ctx.Err() != nil {
				return nil, contextError(ctx)
			}
			// 超时先看是不是掉了登录或出了验证（scraper.py:728-733）。
			if cerr := r.check(ctx, where); cerr != nil {
				return nil, cerr
			}
			if r.S.Capture.SentAfter(mark, m) {
				return nil, fmt.Errorf("%s：请求已发出但没有返回：%w", where, err)
			}
			return nil, fmt.Errorf("%s：没有发出请求：%w", where, err)
		}
		if cerr := r.check(ctx, where); cerr != nil {
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
		r.Log("接口返回", map[string]any{"步骤": where, "ret": ret, "耗时毫秒": time.Since(start).Milliseconds()})
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

// 等待接口时也处理其他接口的风控；页面检查在这里串行执行，不在网络回调里发命令。
func (r *Runner) waitResponse(ctx context.Context, mark uint64, m browser.Match, where string, timeout, quiet time.Duration) (*browser.Response, error) {
	if timeout <= 0 {
		return nil, browser.ErrResponseTimeout
	}
	var resp *browser.Response
	err := r.watch(ctx, where, func(waitCtx context.Context) error {
		var err error
		if quiet > 0 {
			resp, err = r.S.Capture.WaitLastAfter(waitCtx, mark, m, quiet, timeout)
		} else {
			resp, err = r.S.Capture.WaitAfter(waitCtx, mark, m, timeout)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}
