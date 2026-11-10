package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

// runOpts 是 run 子命令的参数，默认值按新号实验方案（EXPERIMENT.md、CRAWL-POLICY 第八节）。
type runOpts struct {
	keyword      string
	interval     time.Duration
	jitter       float64
	pages        int
	maxDetails   int
	publish      int
	quiet        quietHours
	duration     time.Duration
	minGap       time.Duration
	roundTimeout time.Duration
	maxFails     int
}

func cmdRun(ctx context.Context, args []string) int {
	var c common
	var o runOpts
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	c.register(fs)
	fs.StringVar(&o.keyword, "keyword", "", "搜索关键词（必填）")
	fs.DurationVar(&o.interval, "interval", 60*time.Minute, "两轮开始之间的间隔")
	fs.Float64Var(&o.jitter, "jitter", 0.3, "间隔随机延长的最大比例，0.3 表示延长 0–30%")
	fs.IntVar(&o.pages, "pages", 1, "每轮读几页搜索结果")
	fs.IntVar(&o.maxDetails, "max-details", 3, "每轮最多打开几个详情页")
	fs.IntVar(&o.publish, "publish", 0, "发布时间筛选：0 表示「最新」，或 1、3、7、14 天内")
	o.quiet = quietHours{from: 22 * 60, to: 8 * 60, on: true}
	fs.Var(&o.quiet, "quiet", "不运行的时段，比如 22:00-08:00；写 off 表示全天运行")
	fs.DurationVar(&o.duration, "duration", 7*24*time.Hour, "总共跑多久")
	fs.DurationVar(&o.minGap, "min-gap", 3*time.Minute, "上一轮结束到下一轮开始至少隔多久，实际再随机加 0–60 秒")
	fs.DurationVar(&o.roundTimeout, "round-timeout", 15*time.Minute, "一轮最长多久")
	fs.IntVar(&o.maxFails, "max-fails", 3, "连续几轮普通失败就退出")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if o.keyword == "" {
		fmt.Fprintln(os.Stderr, "缺少 -keyword")
		return exitError
	}
	if o.pages < 1 || o.maxDetails < 0 || o.jitter < 0 || o.interval <= 0 {
		fmt.Fprintln(os.Stderr, "参数不对：pages 至少 1，max-details、jitter 不能为负，interval 要大于 0")
		return exitError
	}
	if err := c.prepare(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError
	}
	defer c.j.Close()

	chrome := c.chrome
	if chrome == "" {
		chrome, _ = browser.FindChrome()
	}
	c.j.log("开始实验", map[string]any{
		"关键词": o.keyword, "间隔": o.interval.String(), "随机延长": o.jitter, "翻页": o.pages,
		"详情上限": o.maxDetails, "发布时间": o.publish, "不运行时段": o.quiet.String(), "总时长": o.duration.String(),
		"每小时上限": c.limits.Hourly, "每天搜索上限": c.limits.DailySearch, "每天详情上限": c.limits.DailyDetail,
		"节奏": paceFields(&c), "系统": runtime.GOOS + "/" + runtime.GOARCH, "浏览器": chrome, "代理": c.proxy != "",
	})
	return loop(ctx, &c, o)
}

func loop(ctx context.Context, c *common, o runOpts) int {
	end := time.Now().Add(o.duration)
	next := time.Now()
	fails := 0
	for n := 1; ; n++ {
		next = earliest(next, c.state.LastRunEnd, o)
		if next.After(end) {
			c.j.log("到了设定的运行时长，实验结束", nil)
			return exitOK
		}
		if d := time.Until(next); d > 0 {
			c.j.log("等待下一轮", map[string]any{"轮次": n, "开始时间": next.Format("01-02 15:04:05")})
			select {
			case <-ctx.Done():
				c.j.log("手动停止", nil)
				return exitOK
			case <-time.After(d):
			}
			// 电脑休眠时计时会停，醒来可能已经进了不运行时段或过了截止时间，重新算一遍。
			if now := time.Now(); o.quiet.contains(now) || now.After(end) || now.Before(next) {
				n--
				next = now
				continue
			}
		}

		start := time.Now()
		log := c.j.with(map[string]any{"轮次": n})
		c.state.log = log
		err := round(ctx, c, o, log)
		c.state.LastRunEnd = time.Now()
		if serr := c.state.save(); serr != nil {
			log("保存状态失败", map[string]any{"原因": serr.Error()})
		}
		switch {
		case ctx.Err() != nil:
			log("手动停止", nil)
			return exitOK
		case err == nil:
			fails = 0
		case errors.Is(err, web.ErrQuota):
			// 额度用完不是风控，本轮提前结束，下一轮照常。
			log("额度用完，本轮提前结束", map[string]any{"原因": err.Error()})
			fails = 0
		case web.IsStop(err):
			return c.exitFor(err, log)
		default:
			fails++
			log("本轮失败", map[string]any{"原因": err.Error(), "连续失败": fails})
			if fails >= o.maxFails {
				return c.exitFor(fmt.Errorf("连续 %d 轮失败", fails), log)
			}
		}
		next = start.Add(o.interval + time.Duration(rand.Float64()*o.jitter*float64(o.interval)))
	}
}

// earliest 在 t 的基础上避开不运行时段，并保证和上一轮结束隔够最小间隔。
func earliest(t, lastEnd time.Time, o runOpts) time.Time {
	if gap := lastEnd.Add(o.minGap + time.Duration(rand.Float64()*float64(time.Minute))); t.Before(gap) {
		t = gap
	}
	if o.quiet.contains(t) {
		// 不在整点准时开始，随机推后 0–10 分钟。
		t = o.quiet.end(t).Add(time.Duration(rand.Float64() * float64(10*time.Minute)))
	}
	return t
}

// round 跑一轮：启动浏览器 → 本地确认登录 → 首页 → 搜索 → 发布时间 → 翻页 → 打开最新的几个详情 → 关闭浏览器。
func round(parent context.Context, c *common, o runOpts, log func(string, map[string]any)) error {
	ctx, cancel := context.WithTimeout(parent, o.roundTimeout)
	defer cancel()
	start := time.Now()
	log("本轮开始", nil)

	s, r, err := c.open(ctx, log)
	if err != nil {
		return err
	}
	mem := watchMemory(s.PID(), 5*time.Second)
	sum := map[string]any{}
	err = browse(ctx, r, c, o, log, sum)
	sum["内存峰值MB"] = mem.done()
	s.Close()
	sum["配置目录MB"] = dirSizeMB(c.profileDir())
	sum["耗时秒"] = int(time.Since(start).Seconds())
	if err != nil {
		sum["结果"] = err.Error()
	} else {
		sum["结果"] = "完成"
	}
	log("本轮汇总", sum)
	return err
}

func browse(ctx context.Context, r *web.Runner, c *common, o runOpts, log func(string, map[string]any), sum map[string]any) error {
	if err := r.RequireLoginCookie(); err != nil {
		sum["登录"] = false
		return err
	}
	if err := r.OpenHome(ctx); err != nil {
		return err
	}
	if err := r.RequireLoggedIn(ctx, "首页"); err != nil {
		sum["登录"] = false
		return err
	}
	sum["登录"] = true
	if _, err := r.Search(ctx, o.keyword); err != nil {
		return err
	}
	resp, err := r.SelectPublished(ctx, o.publish)
	if err != nil {
		return err
	}
	items, err := xianyu.ParseSearch(resp.Body)
	if err != nil {
		return fmt.Errorf("解析第 1 页：%w", err)
	}
	log("读到一页", map[string]any{"页": 1, "商品数": len(items)})
	for p := 2; p <= o.pages && len(items) > 0; p++ {
		resp, ok, err := r.NextPage(ctx)
		if web.IsStop(err) || errors.Is(err, web.ErrQuota) {
			return err
		}
		if err != nil {
			// 翻页失败只停止翻页，保留已经读到的页（XIANYU.md 5.6 节）。
			log("翻页失败，不再翻页", map[string]any{"页": p, "原因": err.Error()})
			break
		}
		if !ok {
			break
		}
		more, err := xianyu.ParseSearch(resp.Body)
		if err != nil || len(more) == 0 {
			break
		}
		log("读到一页", map[string]any{"页": p, "商品数": len(more)})
		items = append(items, more...)
	}
	items = uniqueItems(items)

	shown := len(items)
	items = filterPublished(items, o.publish, time.Now())
	picks, fresh := pickDetails(items, c.state.Seen, o.maxDetails)
	// 要打开详情的商品等打开成功后再记为见过；失败或额度不够没打开的，下一轮还能再挑到。
	pending := map[string]bool{}
	for _, it := range picks {
		pending[it.ID] = true
	}
	now := time.Now()
	for _, it := range items {
		if !pending[it.ID] {
			c.state.Seen[it.ID] = now
		}
	}
	sum["商品数"], sum["补筛后"], sum["新商品数"] = shown, len(items), fresh

	opened, failed := 0, 0
	for _, it := range picks {
		d, err := r.OpenDetail(ctx, it.WebURL)
		if web.IsStop(err) {
			sum["打开详情"], sum["详情失败"] = opened, failed
			return err
		}
		if errors.Is(err, web.ErrQuota) {
			log("详情额度用完", map[string]any{"原因": err.Error()})
			break
		}
		if err != nil {
			failed++
			c.state.Failed[it.ID]++
			if c.state.Failed[it.ID] >= 3 {
				c.state.Seen[it.ID] = time.Now()
			}
			log("详情失败", map[string]any{"商品": it.ID, "原因": err.Error(), "第几次": c.state.Failed[it.ID]})
			continue
		}
		opened++
		c.state.Seen[it.ID] = time.Now()
		log("详情", map[string]any{"商品": it.ID, "发布": it.PublishedAt.Format("01-02 15:04"), "有卖家ID": d.SellerID != "", "注册天数": d.RegDays})
	}
	sum["打开详情"], sum["详情失败"] = opened, failed
	return nil
}

// uniqueItems 去掉翻页时重复出现的商品，保留第一次出现的。
func uniqueItems(items []xianyu.SearchItem) []xianyu.SearchItem {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		if !seen[it.ID] {
			seen[it.ID] = true
			out = append(out, it)
		}
	}
	return out
}

// filterPublished 在本地补筛发布时间（XIANYU.md 5.7 节）：要求 N 天内时，早于现在减 N×24 小时的去掉，发布时间解析不出来的也去掉。
// withinDays 为 0（「最新」）时不补筛。
func filterPublished(items []xianyu.SearchItem, withinDays int, now time.Time) []xianyu.SearchItem {
	if withinDays <= 0 {
		return items
	}
	cutoff := now.Add(-time.Duration(withinDays) * 24 * time.Hour)
	var out []xianyu.SearchItem
	for _, it := range items {
		if it.PublishedKnown && !it.PublishedAt.Before(cutoff) {
			out = append(out, it)
		}
	}
	return out
}

// pickDetails 从没见过的商品里挑发布时间最新的 max 个；发布时间未知的排在最后。fresh 是没见过的商品数。
func pickDetails(items []xianyu.SearchItem, seen map[string]time.Time, max int) (picks []xianyu.SearchItem, fresh int) {
	for _, it := range items {
		if _, ok := seen[it.ID]; ok || it.WebURL == "" {
			continue
		}
		picks = append(picks, it)
	}
	fresh = len(picks)
	sort.SliceStable(picks, func(i, j int) bool {
		a, b := picks[i], picks[j]
		if a.PublishedKnown != b.PublishedKnown {
			return a.PublishedKnown
		}
		return a.PublishedAt.After(b.PublishedAt)
	})
	if len(picks) > max {
		picks = picks[:max]
	}
	return picks, fresh
}

func paceFields(c *common) map[string]string {
	return map[string]string{
		"首页停留": (*rangeFlag)(&c.pace.HomeStay).String(), "操作间隔": (*rangeFlag)(&c.pace.Action).String(),
		"翻页等待": (*rangeFlag)(&c.pace.PageWait).String(), "详情前等待": (*rangeFlag)(&c.pace.PreDetail).String(),
		"详情停留": (*rangeFlag)(&c.pace.Detail).String(), "输入间隔": (*rangeFlag)(&c.keyDelay).String(),
	}
}
