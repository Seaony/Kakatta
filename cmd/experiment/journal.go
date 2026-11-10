package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type dayWindow struct {
	Timezone string    `json:"timezone"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
}
type visitState struct {
	Version         int           `json:"version"`
	Visits          []quota.Visit `json:"visits"`
	HighestAt       time.Time     `json:"highest_at"`
	Timezone        string        `json:"timezone"`
	Windows         []dayWindow   `json:"windows"`
	InProgress      bool          `json:"in_progress"`
	EndedAt         time.Time     `json:"ended_at"`
	GapSeconds      int           `json:"gap_seconds"`
	VerifyPending   bool          `json:"verify_pending"`
	VerifyURL       string        `json:"verify_url"`
	VerifyLevel     int           `json:"verify_level"`
	LastCooldownEnd time.Time     `json:"last_cooldown_end"`
	CooldownUntil   time.Time     `json:"cooldown_until"`
	ManualUsed      bool          `json:"manual_used"`
	DeniedUntil     time.Time     `json:"denied_until"`
}
type journal struct {
	mu       sync.Mutex
	path     string
	profile  string
	settings experimentSettings
	state    visitState
	loc      *time.Location
	now      func() time.Time
	unlock   func() error
	saveErr  error
}

func openJournal(dir, profile string, s experimentSettings) (*journal, error) {
	return openJournalAt(dir, profile, s, time.Now)
}

func openJournalAt(dir, profile string, s experimentSettings, clock func() time.Time) (*journal, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	_, dirErr := os.Stat(dir)
	if dirErr != nil && !os.IsNotExist(dirErr) {
		return nil, dirErr
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	unlock, err := lockJournal(filepath.Join(dir, "lock"))
	if err != nil {
		return nil, err
	}
	loc, _ := time.LoadLocation(s.Timezone)
	j := &journal{path: filepath.Join(dir, "visits.json"), profile: profile, settings: s, loc: loc, now: clock, unlock: unlock}
	good := false
	defer func() {
		if !good {
			unlock()
		}
	}()
	b, err := os.ReadFile(j.path)
	if os.IsNotExist(err) {
		_, profileErr := os.Stat(profile)
		if dirErr == nil || !os.IsNotExist(profileErr) {
			return nil, j.broken()
		}
		j.state = visitState{Version: 1, Timezone: s.Timezone, HighestAt: clock().UTC()}
	} else if err != nil {
		return nil, j.broken()
	} else if !validState(b, &j.state) {
		return nil, j.broken()
	}
	now := j.effectiveNow()
	if j.state.Timezone != s.Timezone {
		old, _ := time.LoadLocation(j.state.Timezone)
		start, end := dayBounds(now.In(old))
		window := dayWindow{Timezone: j.state.Timezone, Start: start.UTC(), End: end.UTC()}
		found := false
		for _, w := range j.state.Windows {
			if w == window {
				found = true
			}
		}
		if !found {
			j.state.Windows = append(j.state.Windows, window)
		}
		j.state.Timezone = s.Timezone
	}
	if j.state.InProgress {
		// 不知道崩溃前何时结束，从恢复时重新等待，避免重启跳过间隔。
		j.state.InProgress = false
		j.state.EndedAt = now
		j.state.GapSeconds = max(j.state.GapSeconds, s.AccountGapSeconds)
	}
	if _, err = j.checkpoint(); err != nil {
		return nil, err
	}
	if err = atomicWrite(filepath.Join(dir, "initialized"), []byte("1\n")); err != nil {
		return nil, err
	}
	good = true
	return j, nil
}

func validState(b []byte, state *visitState) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return false
	}
	for _, key := range []string{"version", "visits", "highest_at", "timezone", "windows", "in_progress", "ended_at", "gap_seconds", "verify_pending", "verify_url", "verify_level", "last_cooldown_end", "cooldown_until", "manual_used", "denied_until"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) && key != "visits" && key != "windows" {
			return false
		}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(state) != nil || state.Version != 1 || state.Timezone == "" || state.HighestAt.IsZero() || state.VerifyLevel < 0 || state.GapSeconds < 0 || int64(state.GapSeconds) > math.MaxInt64/int64(time.Second) {
		return false
	}
	if _, err := time.LoadLocation(state.Timezone); err != nil {
		return false
	}
	if state.EndedAt.IsZero() != (state.GapSeconds == 0) || state.EndedAt.After(state.HighestAt) || state.LastCooldownEnd.After(state.HighestAt) {
		return false
	}
	if (state.VerifyPending || !state.CooldownUntil.IsZero() || !state.LastCooldownEnd.IsZero()) && state.VerifyLevel < 1 {
		return false
	}
	for _, visit := range state.Visits {
		if visit.At.IsZero() || visit.At.After(state.HighestAt) || visit.Kind != quota.Search && visit.Kind != quota.Detail {
			return false
		}
	}
	for _, window := range state.Windows {
		_, err := time.LoadLocation(window.Timezone)
		if err != nil || window.Timezone == "" || window.Start.IsZero() || window.End.IsZero() {
			return false
		}
		if !window.Start.Before(window.End) || window.Start.After(state.HighestAt) {
			return false
		}
	}
	return true
}

func dayBounds(now time.Time) (time.Time, time.Time) {
	return quota.DayBounds(now)
}

func (j *journal) broken() error {
	return fmt.Errorf("实验访问记录缺失或损坏，已停止访问。优先恢复整个记录目录的备份；没有备份时，先停止工具，按此前设置的最长冷却、拒绝暂停、账号间隔与 48 小时中的最大值等待，再手动删除整个记录目录 %s 和整个浏览器配置目录 %s。浏览器状态也会清掉，需要重新扫码", filepath.Dir(j.path), j.profile)
}

func (j *journal) effectiveNow() time.Time {
	now := j.now()
	if now.Before(j.state.HighestAt) {
		now = j.state.HighestAt
	}
	return now
}

func (j *journal) checkpoint() (time.Time, error) {
	if j.saveErr != nil {
		return time.Time{}, j.saveErr
	}
	now := j.effectiveNow()
	j.state.HighestAt = now.UTC()
	j.state.Visits = quota.Prune(j.state.Visits, now)
	windows := j.state.Windows[:0]
	for _, window := range j.state.Windows {
		if now.Before(window.End) {
			windows = append(windows, window)
		}
	}
	j.state.Windows = windows
	if !j.state.CooldownUntil.IsZero() && !now.Before(j.state.CooldownUntil) {
		j.state.LastCooldownEnd = j.state.CooldownUntil
		j.state.CooldownUntil = time.Time{}
		j.state.ManualUsed = false
	}
	return now, j.save()
}

func (j *journal) save() error {
	if j.saveErr != nil {
		return j.saveErr
	}
	b, err := json.Marshal(j.state)
	if err == nil {
		err = atomicWrite(j.path, b)
	}
	if err != nil {
		j.saveErr = fmt.Errorf("实验记录写入失败，已停止访问，请停止并重新打开实验工具：%w", err)
	}
	return j.saveErr
}

func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".experiment-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}

func (j *journal) checkQuota(visits []quota.Visit, now time.Time, kind quota.Kind) error {
	if err := quota.Check(visits, now.In(j.loc), kind, j.settings.limits()); err != nil {
		return err
	}
	start, _ := dayBounds(now.In(j.loc))
	for _, window := range j.state.Windows {
		if window.Start.Before(start) {
			start = window.Start
		}
	}
	count := 0
	for _, visit := range visits {
		if visit.Kind == kind && !visit.At.Before(start) {
			count++
		}
	}
	limit := j.settings.DailySearchVisits
	if kind == quota.Detail {
		limit = j.settings.DailyDetailVisits
	}
	if count >= limit {
		return errors.New("旧时区的每日额度尚未恢复")
	}
	return nil
}

func (j *journal) Acquire(kind quota.Kind, what string) error { return j.acquire(kind, what, false) }
func (j *journal) acquire(kind quota.Kind, what string, manual bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now, err := j.checkpoint()
	if err != nil {
		return err
	}
	if kind != quota.Search && kind != quota.Detail {
		return errors.New("访问种类无效")
	}
	if !manual {
		if err := j.checkQuota(j.state.Visits, now, kind); err != nil {
			return fmt.Errorf("%s：%w：%s", what, web.ErrQuota, err)
		}
	}
	j.state.Visits = append(j.state.Visits, quota.Visit{At: now.UTC(), Kind: kind})
	return j.save()
}

func (j *journal) begin(login bool, neededSearch int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now, err := j.checkpoint()
	if err != nil {
		return err
	}
	if j.state.InProgress {
		return errors.New("这个账号已有会话，请先完成或关闭")
	}
	if now.Before(j.state.DeniedUntil) {
		return fmt.Errorf("账号被拒绝访问，暂停到 %s", j.state.DeniedUntil.In(j.loc).Format(time.RFC3339))
	}
	if !login {
		if j.state.VerifyPending {
			return errors.New("这个账号需要验证，请先打开验证画面")
		}
		if now.Before(j.state.CooldownUntil) && j.state.ManualUsed {
			return fmt.Errorf("冷却期内的那次手动运行已使用，请等到 %s", j.state.CooldownUntil.In(j.loc).Format(time.RFC3339))
		}
	}
	if until := j.state.EndedAt.Add(time.Duration(j.state.GapSeconds) * time.Second); now.Before(until) {
		return fmt.Errorf("同一账号还要间隔到 %s", until.In(j.loc).Format(time.RFC3339))
	}
	if j.settings.quiet(now.In(j.loc)) {
		return errors.New("当前是不运行时段，请在设置的时段结束后再试")
	}
	if !login {
		visits := append([]quota.Visit(nil), j.state.Visits...)
		for range neededSearch {
			if err := j.checkQuota(visits, now, quota.Search); err != nil {
				return fmt.Errorf("完整筛选前的额度不足：%w", err)
			}
			visits = append(visits, quota.Visit{At: now.UTC(), Kind: quota.Search})
		}
	}
	if !login && now.Before(j.state.CooldownUntil) {
		j.state.ManualUsed = true
	}
	j.state.InProgress = true
	return j.save()
}

func (j *journal) finish() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now, err := j.checkpoint()
	if err != nil {
		return err
	}
	j.state.InProgress = false
	j.state.EndedAt = now
	j.state.GapSeconds = int(pacer.New(nil).Pick(pacer.Between(time.Duration(j.settings.AccountGapSeconds)*time.Second, time.Duration(j.settings.AccountGapSeconds+j.settings.GapJitterSeconds)*time.Second)) / time.Second)
	return j.save()
}

func (j *journal) risk(err error, verifyURL string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now, saveErr := j.checkpoint()
	if saveErr != nil {
		return saveErr
	}
	if errors.Is(err, web.ErrNeedVerify) {
		if now.Before(j.state.CooldownUntil) || !j.state.LastCooldownEnd.IsZero() && now.Sub(j.state.LastCooldownEnd) < time.Duration(j.settings.CooldownResetHours)*time.Hour {
			if j.state.VerifyLevel < math.MaxInt {
				j.state.VerifyLevel++
			}
		} else {
			j.state.VerifyLevel = 1
		}
		if now.Before(j.state.CooldownUntil) {
			j.state.LastCooldownEnd = now
			j.state.CooldownUntil = time.Time{}
		}
		j.state.VerifyPending = true
		j.state.VerifyURL = verifyURL
	} else if errors.Is(err, web.ErrDenied) || errors.Is(err, web.ErrEnvBlocked) {
		j.state.VerifyPending = false
		j.state.VerifyURL = ""
		if !j.state.CooldownUntil.IsZero() {
			j.state.LastCooldownEnd = now
		}
		j.state.CooldownUntil = time.Time{}
		j.state.ManualUsed = false
		j.state.DeniedUntil = now.Add(time.Duration(j.settings.DenyHours) * time.Hour)
	} else if errors.Is(err, web.ErrLoginExpired) {
		j.state.VerifyPending = false
		j.state.VerifyURL = ""
	}
	return j.save()
}

func (j *journal) verified() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now, err := j.checkpoint()
	if err != nil {
		return err
	}
	minutes := j.settings.CooldownBaseMinutes
	for n := 1; n < j.state.VerifyLevel && minutes < j.settings.CooldownMaxMinutes; n++ {
		if minutes > j.settings.CooldownMaxMinutes/2 {
			minutes = j.settings.CooldownMaxMinutes
		} else {
			minutes *= 2
		}
	}
	j.state.VerifyPending = false
	j.state.VerifyURL = ""
	j.state.ManualUsed = false
	j.state.CooldownUntil = now.Add(time.Duration(minutes) * time.Minute)
	return j.save()
}
