package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func (c *controller) stopManual(r *resources, err error) error {
	if !errors.Is(err, web.ErrDenied) && !errors.Is(err, web.ErrEnvBlocked) && !errors.Is(err, web.ErrLoginExpired) && !errors.Is(err, web.ErrDiagnostics) {
		return err
	}
	err = c.finalError(context.Background(), r, "人工验证", err)
	current, _ := r.chrome.URL()
	err = errors.Join(err, c.journal.risk(err, current), c.detach(r))
	c.phase("failed", err)
	return err
}

func (c *controller) checkManual(ctx context.Context) {
	c.mu.Lock()
	r := c.res
	manual := r != nil && c.state.Phase == "verification" && c.cancelJob == nil
	c.mu.Unlock()
	if manual {
		r.checkMu.Lock()
		defer r.checkMu.Unlock()
		c.stopManual(r, r.runner.CheckVerification(ctx))
	}
}

func (c *controller) holdVerification(r *resources, err error) {
	r.runner.BeginVerification()
	c.phase("verification", err)
	c.focusVerification(r)
	deadline := time.Now().Add(time.Duration(c.settings.VerifyHoldMinutes) * time.Minute)
	if r.remote.HasViewer() {
		deadline = minTime(deadline, time.Now().Add(10*time.Minute))
	}
	c.arm(r, deadline)
	c.log("运行已停止，请手动处理验证码", map[string]any{"保留到": deadline})
}

func (c *controller) verify(ctx context.Context) error {
	c.mu.Lock()
	if c.res != nil && c.state.Phase == "verification" && c.cancelJob == nil {
		r := c.res
		c.mu.Unlock()
		c.viewing(r)
		c.checkManual(ctx)
		return nil
	}
	c.mu.Unlock()
	c.journal.mu.Lock()
	pending, target := c.journal.state.VerifyPending, c.journal.state.VerifyURL
	c.journal.mu.Unlock()
	if !pending || target == "" {
		return errors.New("这个账号没有待处理的验证")
	}
	if c.opts.Fixture {
		u, err := url.Parse(target)
		home, _ := url.Parse(c.site.Home)
		if err != nil || u.Host != home.Host {
			return errors.New("假页面实验不能打开外部地址")
		}
	}
	job, err := c.reserve(ctx, "verify")
	if err != nil {
		return err
	}
	defer c.release()
	c.beginSummary("verify")
	job, stop := context.WithTimeout(job, 10*time.Minute)
	defer stop()
	r, err := c.makeResources(job, true)
	if err != nil {
		err = c.finalError(job, r, "打开验证画面", errors.Join(err, c.journal.finish()))
		c.phase("failed", err)
		return err
	}
	kind := quota.Search
	if strings.Contains(target, "/item?") || strings.Contains(target, "/personal") {
		kind = quota.Detail
	}
	if err = c.journal.acquire(kind, "重新打开验证网址", true); err == nil {
		err = r.chrome.Navigate(target, 30*time.Second)
	}
	if err != nil {
		err = c.finalError(job, r, "打开验证网址", err)
		c.detach(r)
		c.phase("failed", err)
		return err
	}
	r.runner.BeginVerification()
	if err := c.stopManual(r, r.runner.CheckVerification(job)); err != nil {
		err = errors.Join(err, c.detach(r))
		c.phase("failed", err)
		return err
	}
	c.phase("verification", nil)
	c.log("请在同一个远程画面里处理验证；没有看到验证框时自己刷新页面", nil)
	c.focusVerification(r)
	c.arm(r, time.Now().Add(10*time.Minute))
	return nil
}
func (c *controller) confirm(ctx context.Context) error {
	c.mu.Lock()
	r := c.res
	phase := c.state.Phase
	c.mu.Unlock()
	if r == nil || phase != "verification" {
		return errors.New("请先打开这个账号的验证画面")
	}
	r.checkMu.Lock()
	defer r.checkMu.Unlock()
	if err := r.runner.ConfirmVerification(ctx); err != nil {
		return c.stopManual(r, err)
	}
	if err := c.journal.verified(); err != nil {
		return err
	}
	if err := c.detach(r); err != nil {
		return err
	}
	c.phase("completed", nil)
	c.log("验证已完成，账号进入冷却", nil)
	return nil
}
