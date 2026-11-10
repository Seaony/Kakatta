package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func local(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.Local) }

func TestQuietHoursAcrossMidnight(t *testing.T) {
	var q quietHours
	if err := q.Set("22:00-08:00"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{local(21, 59), false}, {local(22, 0), true}, {local(3, 0), true}, {local(8, 0), false}} {
		if q.contains(c.t) != c.want {
			t.Fatalf("%s 应该是 %v", c.t.Format("15:04"), c.want)
		}
	}
	if got := q.end(local(23, 0)); !got.Equal(local(8, 0).AddDate(0, 0, 1)) {
		t.Fatalf("23 点所在时段应在次日 8 点结束：%s", got)
	}
	if got := q.end(local(3, 0)); !got.Equal(local(8, 0)) {
		t.Fatalf("3 点所在时段应在当天 8 点结束：%s", got)
	}
	if err := q.Set("off"); err != nil || q.contains(local(23, 0)) {
		t.Fatal("off 应该全天运行")
	}
}

func TestEarliestRespectsGapAndQuiet(t *testing.T) {
	o := runOpts{minGap: 3 * time.Minute, quiet: quietHours{from: 22 * 60, to: 8 * 60, on: true}}
	got := earliest(local(10, 0), local(9, 59), o)
	if got.Before(local(10, 2)) || got.After(local(10, 3)) {
		t.Fatalf("要和上一轮结束隔 3–4 分钟：%s", got)
	}
	got = earliest(local(23, 0), time.Time{}, o)
	start := local(8, 0).AddDate(0, 0, 1)
	if got.Before(start) || got.After(start.Add(10*time.Minute)) {
		t.Fatalf("不运行时段内应推到次日 8:00–8:10：%s", got)
	}
}

func TestPickDetailsNewestUnseen(t *testing.T) {
	item := func(id string, h int, known bool) xianyu.SearchItem {
		return xianyu.SearchItem{ID: id, WebURL: "https://www.goofish.com/item?id=" + id, PublishedAt: local(h, 0), PublishedKnown: known}
	}
	items := []xianyu.SearchItem{item("a", 9, true), item("b", 11, true), item("c", 0, false), item("d", 10, true), item("e", 12, true)}
	seen := map[string]time.Time{"e": local(12, 0)}
	picks, fresh := pickDetails(items, seen, 3)
	if fresh != 4 {
		t.Fatalf("没见过的有 4 个：%d", fresh)
	}
	var ids string
	for _, p := range picks {
		ids += p.ID
	}
	if ids != "bda" {
		t.Fatalf("应按发布时间从新到旧取 3 个、未知时间排最后：%s", ids)
	}
}

func TestStateBudgetPersistsAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	limits := quota.Limits{Hourly: 2, DailySearch: 10, DailyDetail: 10}
	s, err := loadState(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Acquire(quota.Search, "测试"); err != nil {
			t.Fatal(err)
		}
	}
	// 重新读入后额度照样算上，重启程序不会重置额度。
	s2, err := loadState(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Acquire(quota.Detail, "测试"); !errors.Is(err, web.ErrQuota) {
		t.Fatalf("超过每小时上限应返回 ErrQuota：%v", err)
	}
}

func TestRangeFlag(t *testing.T) {
	var r pacer.Range
	if err := (*rangeFlag)(&r).Set("2s-5s"); err != nil || r.Min != 2*time.Second || r.Max != 5*time.Second {
		t.Fatalf("%v %v", r, err)
	}
	for _, bad := range []string{"5s", "5s-2s", "0s-1s", "a-b"} {
		if err := (*rangeFlag)(&r).Set(bad); err == nil {
			t.Fatalf("应该拒绝 %q", bad)
		}
	}
}

// 在本地假闲鱼上完整跑几轮：正常一轮会打开新商品的详情；第二轮没有新商品就不打开；结果页出验证时以风控结束。
func TestRoundAgainstFakeSite(t *testing.T) {
	if _, err := browser.FindChrome(); err != nil {
		t.Skip("本机没有 Chrome：", err)
	}
	f := fake.Start()
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	fast := pacer.Between(5*time.Millisecond, 10*time.Millisecond)
	c := common{
		site:     web.Site{Home: f.Home(), APIHost: f.APIHost()},
		dir:      t.TempDir(),
		limits:   quota.Limits{Hourly: 100, DailySearch: 100, DailyDetail: 100},
		pace:     web.Pace{HomeStay: fast, Action: fast, PageWait: fast, PreDetail: fast, Detail: fast},
		keyDelay: fast,
	}
	if err := c.prepare(); err != nil {
		t.Fatal(err)
	}
	defer c.j.Close()

	// 先模拟扫码登录，写入登录 Cookie。
	s, err := browser.Start(ctx, browser.Options{ProfileDir: c.profileDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(f.LoginURL(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	s.Close()

	o := runOpts{keyword: "MacBook", pages: 2, maxDetails: 2, roundTimeout: time.Minute}
	if err := round(ctx, &c, o, c.j.log); err != nil {
		t.Fatal(err)
	}
	if err := round(ctx, &c, o, c.j.log); err != nil {
		t.Fatal(err)
	}
	f.SetMaskOnSearch(true)
	err = round(ctx, &c, o, c.j.log)
	if !errors.Is(err, web.ErrNeedVerify) {
		t.Fatalf("应以人机验证结束：%v", err)
	}
	if code := c.exitFor(err, c.j.log); code != exitRisk {
		t.Fatalf("风控的退出码应为 %d：%d", exitRisk, code)
	}

	logPath := filepath.Join(c.dir, "probe.jsonl")
	sums := filter(records(t, logPath), "本轮汇总")
	if len(sums) != 3 {
		t.Fatalf("应有 3 条本轮汇总：%d", len(sums))
	}
	if sums[0]["打开详情"] != float64(2) || sums[0]["商品数"] != float64(3) || sums[0]["登录"] != true {
		t.Fatalf("第一轮应读 2 页、去重后 3 个商品、打开 2 个详情：%v", sums[0])
	}
	if got := field(records(t, logPath), "详情", "商品"); got != "103,101" {
		t.Fatalf("应按发布时间从新到旧打开 103、101：%s", got)
	}
	if sums[1]["打开详情"] != float64(0) || sums[1]["新商品数"] != float64(0) {
		t.Fatalf("第二轮没有新商品，不应打开详情：%v", sums[1])
	}
	if mem, _ := sums[0]["内存峰值MB"].(float64); mem <= 0 {
		t.Fatalf("应记录浏览器内存峰值：%v", sums[0])
	}
}

// records 读出日志里的全部记录。
func records(t *testing.T, path string) []map[string]any {
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []map[string]any
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("日志不是有效的 JSON：%s", sc.Text())
		}
		out = append(out, rec)
	}
	return out
}

func filter(recs []map[string]any, event string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["event"] == event {
			out = append(out, r)
		}
	}
	return out
}

// field 把某种记录里某个字段的值按顺序用逗号连起来。
func field(recs []map[string]any, event, key string) string {
	var out []string
	for _, r := range filter(recs, event) {
		out = append(out, fmt.Sprint(r[key]))
	}
	return strings.Join(out, ",")
}

func TestFilterPublished(t *testing.T) {
	now := local(12, 0)
	items := []xianyu.SearchItem{
		{ID: "new", PublishedAt: now.Add(-time.Hour), PublishedKnown: true},
		{ID: "old", PublishedAt: now.Add(-25 * time.Hour), PublishedKnown: true},
		{ID: "unknown"},
	}
	if got := filterPublished(items, 0, now); len(got) != 3 {
		t.Fatalf("「最新」不补筛：%d", len(got))
	}
	got := filterPublished(items, 1, now)
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("1 天内应只留 new：%v", got)
	}
}
