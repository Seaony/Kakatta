package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type waitRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

func (w waitRange) pace(unit time.Duration) pacer.Range {
	return pacer.Between(time.Duration(w.Min*float64(unit)), time.Duration(w.Max*float64(unit)))
}

type experimentSettings struct {
	Version             int       `json:"version"`
	Timezone            string    `json:"timezone"`
	HourlyVisits        int       `json:"hourly_visits"`
	DailySearchVisits   int       `json:"daily_search_visits"`
	DailyDetailVisits   int       `json:"daily_detail_visits"`
	AccountGapSeconds   int       `json:"account_gap_seconds"`
	GapJitterSeconds    int       `json:"gap_jitter_seconds"`
	HomeSeconds         waitRange `json:"home_seconds"`
	ActionSeconds       waitRange `json:"action_seconds"`
	PageSeconds         waitRange `json:"page_seconds"`
	PreDetailSeconds    waitRange `json:"pre_detail_seconds"`
	DetailSeconds       waitRange `json:"detail_seconds"`
	TypingMS            waitRange `json:"typing_ms"`
	RunTimeoutMinutes   int       `json:"run_timeout_minutes"`
	VerifyHoldMinutes   int       `json:"verify_hold_minutes"`
	CooldownBaseMinutes int       `json:"cooldown_base_minutes"`
	CooldownMaxMinutes  int       `json:"cooldown_max_minutes"`
	CooldownResetHours  int       `json:"cooldown_reset_hours"`
	DenyHours           int       `json:"deny_hours"`
	BrowserCacheMB      int       `json:"browser_cache_mb"`
	QuietEnabled        bool      `json:"quiet_enabled"`
	QuietStart          string    `json:"quiet_start"`
	QuietEnd            string    `json:"quiet_end"`
}

func defaultSettings() experimentSettings {
	tz := os.Getenv("TZ")
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	return experimentSettings{Version: 1, Timezone: tz, HourlyVisits: 30, DailySearchVisits: 200, DailyDetailVisits: 100, AccountGapSeconds: 180, GapJitterSeconds: 60, HomeSeconds: waitRange{2, 5}, ActionSeconds: waitRange{1, 3}, PageSeconds: waitRange{10, 15}, PreDetailSeconds: waitRange{3, 6}, DetailSeconds: waitRange{10, 20}, TypingMS: waitRange{50, 130}, RunTimeoutMinutes: 15, VerifyHoldMinutes: 30, CooldownBaseMinutes: 120, CooldownMaxMinutes: 1440, CooldownResetHours: 24, DenyHours: 24, BrowserCacheMB: 256, QuietEnabled: true, QuietStart: "01:00", QuietEnd: "07:30"}
}

func (s experimentSettings) validate() error {
	if s.Version != 1 {
		return fmt.Errorf("实验设置版本不兼容，请使用更新的程序读取，原文件已保留")
	}
	if s.Timezone == "" {
		return fmt.Errorf("时区不能为空")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("时区无效：%w", err)
	}
	for _, v := range []int{s.HourlyVisits, s.DailySearchVisits, s.DailyDetailVisits, s.AccountGapSeconds, s.RunTimeoutMinutes, s.VerifyHoldMinutes, s.CooldownBaseMinutes, s.CooldownMaxMinutes, s.CooldownResetHours, s.DenyHours, s.BrowserCacheMB} {
		if v < 1 {
			return fmt.Errorf("额度和时间必须为正数")
		}
	}
	if s.GapJitterSeconds < 0 || s.CooldownMaxMinutes < s.CooldownBaseMinutes {
		return fmt.Errorf("随机间隔或冷却范围无效")
	}
	maxSeconds := int64(math.MaxInt64 / int64(time.Second))
	if s.AccountGapSeconds > math.MaxInt-s.GapJitterSeconds || int64(s.AccountGapSeconds) > maxSeconds || int64(s.GapJitterSeconds) > maxSeconds-int64(s.AccountGapSeconds) {
		return fmt.Errorf("账号间隔超过可表示的时间范围")
	}
	for _, v := range []int{s.RunTimeoutMinutes, s.VerifyHoldMinutes, s.CooldownBaseMinutes, s.CooldownMaxMinutes} {
		if int64(v) > math.MaxInt64/int64(time.Minute) {
			return fmt.Errorf("分钟数超过可表示的时间范围")
		}
	}
	for _, v := range []int{s.CooldownResetHours, s.DenyHours} {
		if int64(v) > math.MaxInt64/int64(time.Hour) {
			return fmt.Errorf("小时数超过可表示的时间范围")
		}
	}
	for _, w := range []waitRange{s.HomeSeconds, s.ActionSeconds, s.PageSeconds, s.PreDetailSeconds, s.DetailSeconds} {
		if !validWait(w, time.Second) {
			return fmt.Errorf("等待区间无效")
		}
	}
	if !validWait(s.TypingMS, time.Millisecond) {
		return fmt.Errorf("打字等待区间无效")
	}
	if int64(s.BrowserCacheMB) > math.MaxInt64>>20 {
		return fmt.Errorf("浏览器缓存大小超过可表示的范围")
	}
	for _, clock := range []string{s.QuietStart, s.QuietEnd} {
		parsed, err := time.Parse("15:04", clock)
		if err != nil || parsed.Format("15:04") != clock {
			return fmt.Errorf("不运行时段应写成 HH:mm")
		}
	}
	return nil
}

func validWait(w waitRange, unit time.Duration) bool {
	// 转成整数纳秒之前检查边界，避免浮点数舍入到 2^63 后变成负数。
	return !math.IsNaN(w.Min) && !math.IsNaN(w.Max) && w.Min*float64(unit) >= 1 && w.Max >= w.Min && w.Max*float64(unit) < float64(math.MaxInt64)
}

func loadSettings(path string) (experimentSettings, error) {
	s := defaultSettings()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		if e := s.validate(); e != nil {
			return s, e
		}
		b, e := json.MarshalIndent(s, "", "  ")
		if e != nil {
			return s, e
		}
		return s, atomicWrite(path, append(b, '\n'))
	}
	if err != nil {
		return s, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&s); err != nil {
		return s, fmt.Errorf("实验设置格式不对：%w", err)
	}
	if err = dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return s, fmt.Errorf("实验设置后面不能有其他内容")
	}
	return s, s.validate()
}
func (s experimentSettings) limits() quota.Limits {
	return quota.Limits{Hourly: s.HourlyVisits, DailySearch: s.DailySearchVisits, DailyDetail: s.DailyDetailVisits}
}
func (s experimentSettings) pace() web.Pace {
	return web.Pace{HomeStay: s.HomeSeconds.pace(time.Second), Action: s.ActionSeconds.pace(time.Second), PageWait: s.PageSeconds.pace(time.Second), PreDetail: s.PreDetailSeconds.pace(time.Second), Detail: s.DetailSeconds.pace(time.Second)}
}
func (s experimentSettings) quiet(now time.Time) bool {
	if !s.QuietEnabled || s.QuietStart == s.QuietEnd {
		return false
	}
	clock := now.Format("15:04")
	if s.QuietStart < s.QuietEnd {
		return clock >= s.QuietStart && clock < s.QuietEnd
	}
	return clock >= s.QuietStart || clock < s.QuietEnd
}
