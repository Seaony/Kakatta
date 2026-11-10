package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func cmdLogin(ctx context.Context, args []string) int {
	var c common
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	c.register(fs)
	wait := fs.Duration("wait", 10*time.Minute, "最多等多久完成扫码")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if err := c.prepare(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError
	}
	defer c.j.Close()
	log := c.j.with(map[string]any{"命令": "login"})
	c.state.log = log
	return c.exitFor(login(ctx, &c, log, *wait), log)
}

// login 打开首页，由用户自己点登录、扫码（XIANYU.md 8.2 节）。顺便记下首页顶部的输入框，用来确认搜索框的选择器，不多访问页面。
func login(ctx context.Context, c *common, log func(string, map[string]any), wait time.Duration) error {
	s, r, err := c.open(ctx, log)
	if err != nil {
		return err
	}
	defer s.Close()

	if err := c.state.Acquire(quota.Search, "登录时打开首页"); err != nil {
		return err
	}
	if err := s.Navigate(r.Site.Home, 30*time.Second); err != nil {
		return err
	}
	// 首页是前端渲染的，等输入框出来再记录。
	if _, err := r.H.Find(ctx, "input", 15*time.Second); err != nil {
		log("首页上没有找到输入框", map[string]any{"原因": err.Error()})
	}
	log("首页输入框", map[string]any{"输入框": r.InputFacts(ctx)})

	if ok, _ := r.LoggedIn(ctx); ok {
		log("已经是登录状态", nil)
	} else {
		fmt.Fprintf(os.Stderr, "\n请在打开的浏览器窗口里点右上角「登录」，用闲鱼 App 扫码。最多等 %s。\n\n", wait)
		if err := r.WaitForLogin(ctx, wait); err != nil {
			return err
		}
		log("登录成功", nil)
	}

	head, err := r.OwnProfile(ctx)
	switch {
	case web.IsStop(err):
		return err
	case err != nil:
		log("没有取到昵称", map[string]any{"原因": err.Error()})
	default:
		log("昵称", map[string]any{"昵称": head.Nick})
	}
	return nil
}
