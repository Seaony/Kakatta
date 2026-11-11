package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/experimentstate"
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
	Check                 bool
	WebGL                 string
	Proxy                 *url.URL
	Filters               web.FilterOptions
}
type event = diagnostics.Event
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
	logs     []io.Closer
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
		for _, output := range r.logs {
			output.Close()
		}
	})
}

type controller struct {
	mu        sync.Mutex
	logMu     sync.Mutex
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
	stderr    io.Writer
	recorder  *diagnostics.Recorder
	logErr    error
	summary   *experimentSummary
	bundled   map[string]bool
}

type instantClock struct{}

func (instantClock) Sleep(ctx context.Context, d time.Duration) error { return ctx.Err() }
func (instantClock) Float64() float64                                 { return 0.5 }

type manualBudget struct{ j *journal }

func (b manualBudget) Acquire(kind quota.Kind, what string) error {
	return b.j.acquire(kind, what, true)
}

func newController(o options, s experimentSettings) (*controller, error) {
	branch := "experiment"
	if o.Fixture {
		branch = "experiment-fixture"
		s.QuietEnabled = false
	}
	j, err := openJournal(filepath.Join(o.Data, branch, strconv.Itoa(o.Account)), experimentstate.ProfileDir(o.Data, o.Account, o.Fixture), s)
	if err != nil {
		return nil, err
	}
	recorder, err := diagnostics.Open(experimentLogDir(o), s.Timezone)
	if err != nil {
		j.unlock()
		return nil, err
	}
	c := &controller{recorder: recorder, opts: o, settings: s, journal: j, site: web.Goofish(), state: status{Phase: "idle", Events: []event{}}}
	if o.Fixture {
		c.fixture = fake.Start()
		c.site = web.Site{Home: c.fixture.Home(), APIHost: c.fixture.APIHost()}
	}
	c.log("启动环境", c.environment())
	if err := recorder.Err(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *controller) log(name string, fields map[string]any) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.appendEvent(name, fields)
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
	c.state.Phase = name
	c.state.Running = false
	c.state.Error = diagnosticReason(err)
	c.mu.Unlock()
	fields := map[string]any{"阶段": name}
	if err != nil {
		fields["分类"] = experimentErrorClass(err)
		fields["原因"] = diagnosticReason(err)
	}
	c.log("阶段切换", fields)
	if name == "completed" || name == "failed" || name == "closed" || name == "verification" {
		c.mu.Lock()
		running := c.cancelJob != nil
		c.mu.Unlock()
		if name != "closed" || !running {
			c.finishSummary(name, err)
		}
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
	if err := c.recorder.Err(); err != nil {
		return nil, errors.Join(web.ErrDiagnostics, err)
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
	xvfbLog, err := r.openLog(c.recorder, "xvfb")
	if err != nil {
		return r, err
	}
	r.display, err = browser.StartDisplayWithLog(life, c.opts.Account, xvfbLog)
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
	chromeLog, err := r.openLog(c.recorder, "chrome")
	if err != nil {
		return r, err
	}
	r.chrome, err = browser.Start(life, browser.Options{Output: chromeLog, ProfileDir: experimentstate.ProfileDir(c.opts.Data, c.opts.Account, c.opts.Fixture), AccountID: c.opts.Account, Proxy: proxyAddress, Env: []string{"DISPLAY=" + r.display.Name()}, TimeZone: c.settings.Timezone, CacheMB: c.settings.BrowserCacheMB, WebGL: c.opts.WebGL})
	if err != nil {
		return r, err
	}
	version, versionErr := r.chrome.Version(ctx)
	if versionErr != nil {
		version = "读取失败：" + diagnosticReason(versionErr)
	}
	c.log("浏览器环境", map[string]any{"chrome_version": version, "pid": r.chrome.PID(), "display_pid": r.display.PID()})
	c.mu.Lock()
	if c.summary != nil {
		c.summary.Environment["chrome_version"] = version
	}
	c.mu.Unlock()
	vncLog, err := r.openLog(c.recorder, "x11vnc")
	if err != nil {
		return r, err
	}
	r.remote, err = remote.StartWithFallbackLog(life, r.display.Name(), r.chrome.ScreenshotJPEG, vncLog)
	if err != nil {
		return r, err
	}
	c.log("远程画面状态", map[string]any{"可操作": r.remote.Address() != "", "只读备用": r.remote.Address() == ""})
	var budget web.Budget = c.journal
	if manual {
		budget = manualBudget{c.journal}
	}
	p := pacer.New(nil)
	if c.opts.Fixture {
		p = pacer.New(instantClock{})
	}
	r.runner = web.NewRunner(r.chrome, p, c.settings.TypingMS.pace(time.Millisecond), c.site, c.settings.pace(), diagnosticBudget{inner: budget, recorder: c.recorder}, c.log)
	r.runner.Diagnose = func(ctx context.Context, d web.Diagnostic) error { return c.diagnose(ctx, r, d) }
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
	c.log("阶段切换", map[string]any{"阶段": "closed", "分类": "timeout", "原因": "验证会话时间已到"})
	c.finishSummary("closed", errors.New("验证会话时间已到"))
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
		c.log("操作停止", map[string]any{"步骤": "login", "分类": experimentErrorClass(err), "原因": diagnosticReason(err)})
		return err
	}
	return c.loginReserved(job)
}
func (c *controller) loginReserved(job context.Context) (err error) {
	defer c.release()
	c.beginSummary("login")
	job, stop := context.WithTimeout(job, 10*time.Minute)
	defer stop()
	var r *resources
	err = c.recordStep("启动浏览器", func() error { var failure error; r, failure = c.makeResources(job, true); return failure })
	if err != nil {
		err = c.finalError(job, r, "启动浏览器", errors.Join(err, c.journal.finish()))
		c.phase("failed", err)
		return err
	}
	stopBrowser := context.AfterFunc(job, r.chrome.Close)
	defer stopBrowser()
	err = c.recordStep("打开登录首页", func() error { return r.runner.OpenLogin(job) })
	if err == nil {
		r.runner.BeginVerification()
		err = r.runner.CheckVerification(job)
	}
	if err == nil {
		c.phase("ready", nil)
		c.log("请在远程画面里点击登录并扫码", nil)
		err = c.recordStep("等待扫码登录", func() error { return r.runner.WaitForLogin(job, 10*time.Minute) })
	}
	if err == nil {
		var head xianyu.SellerHead
		err = c.recordStep("读取自己的主页", func() error { var failure error; head, failure = r.runner.OwnProfile(job); return failure })
		if err != nil && !web.IsStop(err) {
			c.log("未读到昵称", map[string]any{"原因": err.Error()})
			err = nil
		} else if err == nil {
			c.log("登录完成", map[string]any{"昵称": head.Nick, "有头像": head.Avatar != ""})
		}
	}
	err = c.finalError(job, r, "登录流程", err)
	if web.IsStop(err) {
		current, _ := r.chrome.URL()
		if e := c.journal.risk(err, current); e != nil {
			err = errors.Join(err, e)
		}
	}
	if errors.Is(err, web.ErrNeedVerify) && !errors.Is(err, web.ErrDiagnostics) {
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
	if failure := c.recorder.Err(); failure != nil {
		err = errors.Join(err, web.ErrDiagnostics, failure)
	}
	return err
}
func (c *controller) run(ctx context.Context) error {
	job, err := c.reserve(ctx, "run")
	if err != nil {
		c.log("操作停止", map[string]any{"步骤": "run", "分类": experimentErrorClass(err), "原因": diagnosticReason(err)})
		return err
	}
	return c.runReserved(job)
}
func (c *controller) runReserved(job context.Context) (err error) {
	defer c.release()
	c.beginSummary("run")
	job, stop := context.WithTimeout(job, time.Duration(c.settings.RunTimeoutMinutes)*time.Minute)
	defer stop()
	var r *resources
	err = c.recordStep("启动浏览器", func() error { var failure error; r, failure = c.makeResources(job, false); return failure })
	if err != nil {
		err = c.finalError(job, r, "启动浏览器", errors.Join(err, c.journal.finish()))
		c.phase("failed", err)
		return err
	}
	stopBrowser := context.AfterFunc(job, r.chrome.Close)
	c.phase("running", nil)
	c.mu.Lock()
	c.state.Running = true
	c.mu.Unlock()
	err = c.flow(job, r.runner)
	err = c.finalError(job, r, "单次搜索流程", err)
	stopBrowser()

	if web.IsStop(err) {
		current, _ := r.chrome.URL()
		if saveErr := c.journal.risk(err, current); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}
	if errors.Is(err, web.ErrNeedVerify) && !errors.Is(err, web.ErrDiagnostics) {
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
	if failure := c.recorder.Err(); failure != nil {
		err = errors.Join(err, web.ErrDiagnostics, failure)
	}
	return err
}
func (c *controller) flow(ctx context.Context, r *web.Runner) error {
	if err := c.recordStep("打开首页", func() error { return r.OpenHome(ctx) }); err != nil {
		return err
	}
	inputs := r.InputFacts(ctx)
	c.log("首页搜索框检查", map[string]any{"找到": len(inputs) > 0, "候选": inputs})
	if len(inputs) == 0 {
		return r.ReportFailure(ctx, "首页搜索框", fmt.Errorf("%w：首页没有找到搜索框", browser.ErrNotFound), nil)
	}
	if err := c.recordStep("提交搜索", func() error { _, failure := r.Search(ctx, c.opts.Keyword); return failure }); err != nil {
		return err
	}
	var response *browser.Response
	err := c.recordStep("应用筛选", func() error {
		var failure error
		response, failure = r.ApplyFilters(ctx, c.opts.Filters)
		return failure
	})
	if err != nil {
		return err
	}
	var items []xianyu.SearchItem
	err = c.recordStep("解析搜索", func() error {
		var failure error
		items, failure = xianyu.ParseSearch(response.Body)
		if failure != nil {
			return r.ReportFailure(ctx, "解析搜索", failure, response)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		image := ""
		if u, err := url.Parse(item.Image); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" {
			clean := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
			image = clean.String()
		}
		c.log("搜索商品字段", map[string]any{"编号": item.ID, "标题": item.Title, "价格分": item.PriceCents, "价格已知": item.PriceKnown, "地区": item.Area, "发布时间": item.PublishedAt, "卖家昵称": item.SellerNick, "包邮": item.FreeShip, "主图": image})
	}
	if len(items) < 2 {
		return r.ReportFailure(ctx, "筛选结果", errors.New("筛选后不足两件商品，本次未打开详情，请换一个关键词"), nil)
	}
	var detailErrors []error
	for _, item := range items[:min(c.opts.Details, len(items))] {
		var detail xianyu.Detail
		err := c.recordStep("读取详情："+item.ID, func() error { var failure error; detail, failure = r.OpenDetail(ctx, item.WebURL); return failure })
		if err != nil {
			err = r.ReportFailure(ctx, "读取详情："+item.ID, err, nil)
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
	if c.closed {
		c.mu.Unlock()
		return
	}
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
	if c.recorder != nil {
		if err := c.recorder.Close(); err != nil {
			c.failLogging(err)
		}
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
