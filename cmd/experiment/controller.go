package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/proxyforward"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type options struct {
	Data, Listen, Keyword string
	Account, Details      int
	Fixture, Once         bool
	WebGL                 string
	Proxy                 *url.URL
	Filters               web.FilterOptions
}
type event struct {
	At     time.Time      `json:"at"`
	Event  string         `json:"event"`
	Fields map[string]any `json:"fields,omitempty"`
}
type status struct {
	Viewport      *remote.Viewport `json:"viewport,omitempty"`
	Phase         string           `json:"phase"`
	Running       bool             `json:"running"`
	Events        []event          `json:"events"`
	Error         string           `json:"error,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	NextAllowedAt string           `json:"next_allowed_at,omitempty"`
	VerifyPending bool             `json:"verification_pending"`
}
type resources struct {
	mu       sync.Mutex
	checkMu  sync.Mutex
	closed   bool
	deadline time.Time
	display  *browser.Display
	chrome   *browser.Session
	proxy    *proxyforward.Forwarder
	remote   *remote.Session
	runner   *web.Runner
	cancel   context.CancelFunc
	timer    *time.Timer
	once     sync.Once
}

func (r *resources) close() {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		if r.timer != nil {
			r.timer.Stop()
		}
		r.mu.Unlock()
		if r.remote != nil {
			r.remote.Close()
		}
		if r.chrome != nil {
			r.chrome.Close()
		}
		if r.proxy != nil {
			r.proxy.Close()
		}
		if r.display != nil {
			r.display.Close()
		}
		if r.cancel != nil {
			r.cancel()
		}
	})
}

type controller struct {
	mu        sync.Mutex
	jobs      sync.WaitGroup
	cleanup   sync.WaitGroup
	closing   bool
	opts      options
	settings  experimentSettings
	journal   *journal
	fixture   *fake.Xianyu
	site      web.Site
	res       *resources
	cancelJob context.CancelFunc
	closed    bool
	state     status
	sink      io.Writer
}

type instantClock struct{}

func (instantClock) Sleep(ctx context.Context, d time.Duration) error { return ctx.Err() }
func (instantClock) Float64() float64                                 { return 0.5 }

type manualBudget struct{ j *journal }

func (b manualBudget) Acquire(kind quota.Kind, what string) error {
	return b.j.acquire(kind, what, true)
}

func newController(o options, s experimentSettings) (*controller, error) {
	branch, profiles := "experiment", "browser"
	if o.Fixture {
		branch, profiles = "experiment-fixture", "browser-fixture"
		s.QuietEnabled = false
	}
	j, err := openJournal(filepath.Join(o.Data, branch, strconv.Itoa(o.Account)), filepath.Join(o.Data, profiles, strconv.Itoa(o.Account)), s)
	if err != nil {
		return nil, err
	}
	c := &controller{opts: o, settings: s, journal: j, site: web.Goofish(), state: status{Phase: "idle", Events: []event{}}}
	if o.Fixture {
		c.fixture = fake.Start()
		c.site = web.Site{Home: c.fixture.Home(), APIHost: c.fixture.APIHost()}
	}
	return c, nil
}
func (c *controller) log(name string, fields map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Events = append(c.state.Events, event{At: time.Now().UTC(), Event: name, Fields: fields})
	if c.sink != nil {
		json.NewEncoder(c.sink).Encode(c.state.Events[len(c.state.Events)-1])
	}
	if len(c.state.Events) > 200 {
		c.state.Events = append([]event(nil), c.state.Events[len(c.state.Events)-200:]...)
	}
}
func (c *controller) snapshot() status {
	c.mu.Lock()
	s := c.state
	s.Events = append([]event(nil), s.Events...)
	c.mu.Unlock()
	c.journal.mu.Lock()
	defer c.journal.mu.Unlock()
	s.VerifyPending = c.journal.state.VerifyPending
	now := c.journal.effectiveNow()
	next := c.journal.state.EndedAt.Add(time.Duration(c.journal.state.GapSeconds) * time.Second)
	if now.Before(next) {
		s.Reason = "同一账号的两次会话需要间隔"
		s.NextAllowedAt = next.Format(time.RFC3339)
	}
	if now.Before(c.journal.state.CooldownUntil) && c.journal.state.ManualUsed {
		s.Reason = "冷却期内的那次手动运行已使用"
		s.NextAllowedAt = c.journal.state.CooldownUntil.Format(time.RFC3339)
	}
	if now.Before(c.journal.state.DeniedUntil) {
		s.Reason = "账号被拒绝访问，暂停期间不能打开浏览器"
		s.NextAllowedAt = c.journal.state.DeniedUntil.Format(time.RFC3339)
	}
	return s
}
func (c *controller) phase(name string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Phase = name
	c.state.Running = false
	c.state.Error = ""
	if err != nil {
		c.state.Error = err.Error()
	}
}
func (c *controller) reserve(ctx context.Context, mode string) (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("实验工具已关闭")
	}
	if c.cancelJob != nil || c.res != nil || c.closing {
		return nil, errors.New("这个账号已有会话，请先完成或关闭")
	}
	if err := c.journal.begin(mode != "run", c.minimumSearchVisits()); err != nil {
		return nil, err
	}
	job, cancel := context.WithCancel(ctx)
	c.jobs.Add(1)
	c.cancelJob = cancel
	c.state.Viewport = nil
	c.state.Phase = "opening"
	c.state.Error = ""
	c.state.Running = mode == "run"
	return job, nil
}
func (c *controller) release() {
	c.mu.Lock()
	cancel := c.cancelJob
	c.cancelJob = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.jobs.Done()
}
func (c *controller) makeResources(ctx context.Context, manual bool) (r *resources, err error) {
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r = &resources{cancel: cancel}
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer func() {
		if err != nil {
			r.close()
		}
	}()
	r.display, err = browser.StartDisplay(life, c.opts.Account)
	if err != nil {
		return r, err
	}
	proxyAddress := ""
	if c.opts.Proxy != nil {
		r.proxy, err = proxyforward.Start(life, c.opts.Proxy)
		if err != nil {
			return r, err
		}
		proxyAddress = r.proxy.URL()
	}
	profile := "browser"
	if c.opts.Fixture {
		profile = "browser-fixture"
	}
	r.chrome, err = browser.Start(life, browser.Options{ProfileDir: filepath.Join(c.opts.Data, profile, strconv.Itoa(c.opts.Account)), AccountID: c.opts.Account, Proxy: proxyAddress, Env: []string{"DISPLAY=" + r.display.Name()}, TimeZone: c.settings.Timezone, CacheMB: c.settings.BrowserCacheMB, WebGL: c.opts.WebGL})
	if err != nil {
		return r, err
	}
	r.remote, err = remote.StartWithFallback(life, r.display.Name(), r.chrome.ScreenshotJPEG)
	if err != nil {
		return r, err
	}
	var budget web.Budget = c.journal
	if manual {
		budget = manualBudget{c.journal}
	}
	p := pacer.New(nil)
	if c.opts.Fixture {
		p = pacer.New(instantClock{})
	}
	r.runner = web.NewRunner(r.chrome, p, c.settings.TypingMS.pace(time.Millisecond), c.site, c.settings.pace(), budget, c.log)
	if c.opts.Fixture {
		if err = r.chrome.Navigate(c.fixture.LoginURL(), 5*time.Second); err != nil {
			return r, err
		}
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return r, errors.New("实验工具已关闭")
	}
	c.res = r
	c.mu.Unlock()
	return r, nil
}
func (c *controller) detach(r *resources) error {
	c.mu.Lock()
	active := c.res == r
	if active {
		c.res = nil
		c.closing = true
		c.cleanup.Add(1)
	}
	c.mu.Unlock()
	r.close()
	var err error
	if active {
		err = c.journal.finish()
		c.mu.Lock()
		c.closing = false
		c.mu.Unlock()
		c.cleanup.Done()
	}
	return err
}
func (c *controller) expire(r *resources) {
	c.mu.Lock()
	if c.res != r || c.closed {
		c.mu.Unlock()
		return
	}
	c.state.Phase = "closed"
	c.state.Running = false
	c.state.Error = "验证会话时间已到，请重新打开验证画面"
	c.mu.Unlock()
	if err := c.detach(r); err != nil {
		c.log("保存会话结束时间失败", map[string]any{"原因": err.Error()})
	}
}
func (c *controller) arm(r *resources, deadline time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.deadline.IsZero() && !deadline.Before(r.deadline) {
		return
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	r.deadline = deadline
	r.timer = time.AfterFunc(max(time.Until(deadline), 0), func() { c.expire(r) })
}
func (c *controller) viewing(r *resources) {
	c.mu.Lock()
	verifying := c.res == r && c.state.Phase == "verification"
	c.mu.Unlock()
	if verifying {
		c.arm(r, time.Now().Add(10*time.Minute))
	}
}

func (c *controller) login(ctx context.Context) error {
	job, err := c.reserve(ctx, "login")
	if err != nil {
		return err
	}
	return c.loginReserved(job)
}
func (c *controller) loginReserved(job context.Context) (err error) {
	defer c.release()
	job, stop := context.WithTimeout(job, 10*time.Minute)
	defer stop()
	r, err := c.makeResources(job, true)
	if err != nil {
		err = errors.Join(err, c.journal.finish())
		c.phase("failed", err)
		return err
	}
	stopBrowser := context.AfterFunc(job, r.chrome.Close)
	defer stopBrowser()
	err = r.runner.OpenHome(job)
	if errors.Is(err, web.ErrNeedVerify) {
		c.log("登录页面需要验证，请在当前远程画面手动处理", nil)
		err = nil
	}
	if err == nil {
		r.runner.BeginVerification()
		c.phase("ready", nil)
		c.log("请在远程画面里点击登录并扫码", nil)
		err = r.runner.WaitForLogin(job, 10*time.Minute)
	}
	if err == nil {
		var head xianyu.SellerHead
		head, err = r.runner.OwnProfile(job)
		if err != nil && !web.IsStop(err) {
			c.log("未读到昵称", map[string]any{"原因": err.Error()})
			err = nil
		} else if err == nil {
			c.log("登录完成", map[string]any{"昵称": head.Nick, "有头像": head.Avatar != ""})
		}
	}
	if web.IsStop(err) {
		current, _ := r.chrome.URL()
		if e := c.journal.risk(err, current); e != nil {
			err = errors.Join(err, e)
		}
	}
	if errors.Is(err, web.ErrNeedVerify) {
		stopBrowser()
		c.holdVerification(r, err)
		return err
	}
	if e := c.detach(r); e != nil {
		err = errors.Join(err, e)
	}
	if err != nil {
		c.phase("failed", err)
	} else {
		c.phase("completed", nil)
	}
	return err
}
func (c *controller) run(ctx context.Context) error {
	job, err := c.reserve(ctx, "run")
	if err != nil {
		return err
	}
	return c.runReserved(job)
}
func (c *controller) runReserved(job context.Context) (err error) {
	defer c.release()
	job, stop := context.WithTimeout(job, time.Duration(c.settings.RunTimeoutMinutes)*time.Minute)
	defer stop()
	r, err := c.makeResources(job, false)
	if err != nil {
		err = errors.Join(err, c.journal.finish())
		c.phase("failed", err)
		return err
	}
	stopBrowser := context.AfterFunc(job, r.chrome.Close)
	c.mu.Lock()
	c.state.Phase = "running"
	c.state.Running = true
	c.mu.Unlock()
	err = c.flow(job, r.runner)
	stopBrowser()

	if web.IsStop(err) {
		current, _ := r.chrome.URL()
		if saveErr := c.journal.risk(err, current); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}
	if errors.Is(err, web.ErrNeedVerify) {
		c.holdVerification(r, err)
		return err
	}
	if e := c.detach(r); e != nil {
		err = errors.Join(err, e)
	}
	if err != nil {
		c.phase("failed", err)
	} else {
		c.phase("completed", nil)
	}
	return err
}
func (c *controller) flow(ctx context.Context, r *web.Runner) error {
	if err := r.OpenHome(ctx); err != nil {
		return err
	}
	inputs := r.InputFacts(ctx)
	c.log("首页搜索框检查", map[string]any{"找到": len(inputs) > 0, "候选": inputs})
	if len(inputs) == 0 {
		return errors.New("首页没有找到搜索框")
	}
	if _, err := r.Search(ctx, c.opts.Keyword); err != nil {
		return err
	}
	response, err := r.ApplyFilters(ctx, c.opts.Filters)
	if err != nil {
		return err
	}
	items, err := xianyu.ParseSearch(response.Body)
	if err != nil {
		return err
	}
	for _, item := range items {
		c.log("搜索商品字段", map[string]any{"编号": item.ID, "标题": item.Title, "价格分": item.PriceCents, "价格已知": item.PriceKnown, "地区": item.Area, "发布时间": item.PublishedAt, "卖家昵称": item.SellerNick, "包邮": item.FreeShip, "主图": item.Image})
	}
	if len(items) < 2 {
		return errors.New("筛选后不足两件商品，本次未打开详情，请换一个关键词")
	}
	var detailErrors []error
	for _, item := range items[:min(c.opts.Details, len(items))] {
		detail, err := r.OpenDetail(ctx, item.WebURL)
		if err != nil {
			c.log("详情失败", map[string]any{"商品": item.ID, "原因": err.Error()})
			if web.IsStop(err) || ctx.Err() != nil {
				return err
			}
			detailErrors = append(detailErrors, fmt.Errorf("详情流程未完成：%w", err))
			continue
		}
		c.log("详情字段", map[string]any{"商品": item.ID, "卖家编号": detail.SellerID, "注册天数": detail.RegDays, "信用文字": detail.Zhima, "图片数": len(detail.Images), "描述": detail.Description, "想要人数": detail.WantCnt, "浏览量": detail.BrowseCnt})
	}
	c.log("单次实验流程完成", map[string]any{"详情数": min(c.opts.Details, len(items)), "失败数": len(detailErrors)})
	return errors.Join(detailErrors...)
}
func (c *controller) closeSession() {
	c.mu.Lock()
	r := c.res
	cancel := c.cancelJob
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if r != nil {
		c.detach(r)
	}
	c.phase("closed", nil)
}
func (c *controller) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.closeSession()
	c.jobs.Wait()
	c.cleanup.Wait()
	if c.fixture != nil {
		c.fixture.Close()
	}
	if c.journal != nil {
		c.journal.unlock()
	}
}

func (c *controller) minimumSearchVisits() int {
	n := 3
	if c.opts.Filters.PersonalOnly {
		n++
	}
	if c.opts.Filters.FreeShipping {
		n++
	}
	if c.opts.Filters.Province != "" {
		n += 4
		if c.opts.Filters.District != "" {
			n++
		}
	}
	if c.opts.Filters.MinPriceCents != nil || c.opts.Filters.MaxPriceCents != nil {
		n++
	}
	return n
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
