package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func testSettings() experimentSettings {
	s := defaultSettings()
	s.Timezone = "UTC"
	s.QuietEnabled = false
	s.GapJitterSeconds = 0
	return s
}
func testJournal(t *testing.T, s experimentSettings) *journal {
	t.Helper()
	base := t.TempDir()
	j, err := openJournalAt(filepath.Join(base, "state"), filepath.Join(base, "profile"), s, func() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.unlock() })
	return j
}
func TestJournalQuotaAndGapSurviveRestart(t *testing.T) {
	s := testSettings()
	s.HourlyVisits = 2
	j := testJournal(t, s)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.begin(false, 3); err == nil {
		t.Fatal("筛选前额度不足不能启动")
	}
	for range 2 {
		if err := j.Acquire(quota.Search, "搜索"); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.finish(); err != nil {
		t.Fatal(err)
	}
	j.unlock()
	restored, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.unlock()
	restored.now = func() time.Time { return now }
	if err := restored.Acquire(quota.Detail, "详情"); !errors.Is(err, web.ErrQuota) {
		t.Fatalf("重启不能清小时额度：%v", err)
	}
	if err := restored.begin(true, 0); err == nil {
		t.Fatal("重启不能清账号最小间隔")
	}
}
func TestJournalMissingOrDamagedNeverResetsQuota(t *testing.T) {
	for _, damage := range []string{"missing", "invalid"} {
		t.Run(damage, func(t *testing.T) {
			j := testJournal(t, testSettings())
			j.unlock()
			if damage == "missing" {
				os.Remove(j.path)
			} else {
				os.WriteFile(j.path, []byte("broken"), 0600)
			}
			if next, err := openJournal(filepath.Dir(j.path), j.profile, testSettings()); err == nil {
				next.unlock()
				t.Fatal("损坏记录被当成了零额度")
			}
		})
	}
	base := t.TempDir()
	profile := filepath.Join(base, "profile")
	os.Mkdir(profile, 0700)
	if j, err := openJournal(filepath.Join(base, "state"), profile, testSettings()); err == nil {
		j.unlock()
		t.Fatal("已有浏览器而无记录，不能当作新装")
	}
}
func TestJournalChangingTimezoneKeepsOldDayUntilMidnight(t *testing.T) {
	s := testSettings()
	s.DailySearchVisits = 2
	j := testJournal(t, s)
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	for range 2 {
		if err := j.Acquire(quota.Search, "搜索"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(10 * time.Hour)
	j.state.HighestAt = now
	j.save()
	j.unlock()
	s.Timezone = "Asia/Shanghai"
	next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	defer next.unlock()
	next.now = func() time.Time { return now }
	if err = next.Acquire(quota.Search, "搜索"); !errors.Is(err, web.ErrQuota) {
		t.Fatalf("改到已过零点的时区也不能恢复旧日额度：%v", err)
	}
	now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if err = next.Acquire(quota.Search, "搜索"); err != nil {
		t.Fatalf("旧日结束后应恢复：%v", err)
	}
}
func TestJournalClockRollbackAndCooldownDoubling(t *testing.T) {
	j := testJournal(t, testSettings())
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.Acquire(quota.Search, "搜索"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Hour)
	if !j.effectiveNow().Equal(j.state.HighestAt) {
		t.Fatal("倒退的时钟绕过最高已观察时间")
	}
	now = now.Add(time.Hour)
	if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
		t.Fatal(err)
	}
	if err := j.verified(); err != nil {
		t.Fatal(err)
	}
	if got := j.state.CooldownUntil.Sub(now); got != 2*time.Hour {
		t.Fatalf("首次冷却：%s", got)
	}
	now = j.state.CooldownUntil.Add(time.Hour)
	if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
		t.Fatal(err)
	}
	if err := j.verified(); err != nil {
		t.Fatal(err)
	}
	if got := j.state.CooldownUntil.Sub(now); got != 4*time.Hour {
		t.Fatalf("距冷却结束不足一天应加倍：%s", got)
	}
	if err := j.begin(false, 1); err != nil {
		t.Fatal(err)
	}
	if err := j.begin(false, 1); err == nil {
		t.Fatal("冷却期内不能反复手动运行")
	}
}
func TestJournalRejectsSecondWriter(t *testing.T) {
	j := testJournal(t, testSettings())
	if next, err := openJournal(filepath.Dir(j.path), j.profile, testSettings()); err == nil {
		next.unlock()
		t.Fatal("不能并发写同一账号的额度")
	}
}

func TestJournalInterruptedSessionStartsGapAtRecovery(t *testing.T) {
	s := testSettings()
	j := testJournal(t, s)
	now := time.Now().Add(-time.Hour)
	j.now = func() time.Time { return now }
	if err := j.begin(true, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(j.path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err = json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["in_progress"] != true {
		t.Error("启动前没有持久化会话标志")
	}
	j.unlock()
	recoveredAt := time.Now()
	next, err := openJournal(filepath.Dir(j.path), j.profile, s)
	if err != nil {
		t.Fatal(err)
	}
	defer next.unlock()
	if next.state.EndedAt.Before(recoveredAt) {
		t.Errorf("崩溃后间隔未从恢复时开始：%s", next.state.EndedAt)
	}
	if err = next.begin(true, 0); err == nil {
		t.Error("崩溃重启跳过了账号最小间隔")
	}
}

func TestJournalDeniedAlsoBlocksManualSessions(t *testing.T) {
	j := testJournal(t, testSettings())
	if err := j.risk(web.ErrDenied, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.begin(true, 0); err == nil {
		t.Fatal("登录或验证会话绕过了拒绝访问暂停")
	}
}

func TestJournalDenialReplacesPendingVerification(t *testing.T) {
	for _, risk := range []error{web.ErrDenied, web.ErrEnvBlocked} {
		t.Run(risk.Error(), func(t *testing.T) {
			j := testJournal(t, testSettings())
			if err := j.risk(web.ErrNeedVerify, "http://127.0.0.1/punish"); err != nil {
				t.Fatal(err)
			}
			if err := j.risk(risk, "http://127.0.0.1/punish?action=deny"); err != nil {
				t.Fatal(err)
			}
			if j.state.VerifyPending || j.state.VerifyURL != "" {
				t.Fatal("拒绝访问后仍残留待验证状态")
			}
			after := j.state.DeniedUntil.Add(time.Second)
			j.now = func() time.Time { return after }
			if err := j.begin(false, 0); err != nil {
				t.Fatalf("拒绝暂停结束后不能恢复：%v", err)
			}
		})
	}
}

func TestJournalExpiredDoesNotResetQuotaOrCooldown(t *testing.T) {
	j := testJournal(t, testSettings())
	if err := j.Acquire(quota.Search, "首页"); err != nil {
		t.Fatal(err)
	}
	if err := j.risk(web.ErrNeedVerify, "http://127.0.0.1/punish"); err != nil {
		t.Fatal(err)
	}
	if err := j.verified(); err != nil {
		t.Fatal(err)
	}
	until := j.state.CooldownUntil
	if err := j.risk(web.ErrLoginExpired, ""); err != nil {
		t.Fatal(err)
	}
	if len(j.state.Visits) != 1 || j.state.CooldownUntil != until {
		t.Fatal("登录失效清掉了访问记录或原有冷却")
	}
}

func TestJournalCooldownEndIsRecordedBeforeNextVerification(t *testing.T) {
	j := testJournal(t, testSettings())
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
		t.Fatal(err)
	}
	if err := j.verified(); err != nil {
		t.Fatal(err)
	}
	end := j.state.CooldownUntil
	now = end.Add(time.Hour)
	if err := j.begin(false, 1); err != nil {
		t.Fatal(err)
	}
	if !j.state.LastCooldownEnd.Equal(end) || !j.state.CooldownUntil.IsZero() {
		t.Fatalf("自然结束的冷却没有归档：%+v", j.state)
	}
}

func TestJournalRejectsIncompleteRecord(t *testing.T) {
	j := testJournal(t, testSettings())
	j.unlock()
	if err := os.WriteFile(j.path, []byte(`{"version":1,"timezone":"UTC"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if next, err := openJournal(filepath.Dir(j.path), j.profile, testSettings()); err == nil {
		next.unlock()
		t.Fatal("字段缺失的记录被当成了零额度")
	}
}

func TestJournalSaveFailureKeepsAccessBlocked(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "普通访问", true: "人工会话"}[manual], func(t *testing.T) {
			j := testJournal(t, testSettings())
			original := j.path
			j.path = filepath.Join(filepath.Dir(original), "missing", "visits.json")
			if err := j.acquire(quota.Search, "搜索", manual); err == nil {
				t.Fatal("写盘失败仍允许访问")
			}
			j.path = original
			if err := j.begin(true, 0); err == nil {
				t.Fatal("写盘失败后同一会话仍继续访问")
			}
		})
	}
}

func TestLoadSettingsRejectsInvalidDefaultAndTrailingJSON(t *testing.T) {
	t.Run("首次配置时区", func(t *testing.T) {
		t.Setenv("TZ", "bad/zone")
		path := filepath.Join(t.TempDir(), "settings.json")
		if _, err := loadSettings(path); err == nil {
			t.Fatal("首次生成配置没有校验无效时区")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("无效默认配置不应落盘")
		}
	})
	for _, trailing := range []string{"{}", "null", "garbage"} {
		t.Run("尾随"+trailing, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(`{"timezone":"UTC"}`+trailing), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadSettings(path); err == nil {
				t.Fatal("接受了尾随 JSON 或垃圾字符")
			}
		})
	}
}

func TestSettingsRejectsDurationOverflow(t *testing.T) {
	for name, change := range map[string]func(*experimentSettings){
		"冷却":   func(s *experimentSettings) { s.CooldownBaseMinutes, s.CooldownMaxMinutes = 153722868, 153722868 },
		"最长冷却": func(s *experimentSettings) { s.CooldownMaxMinutes = int(math.MaxInt64/int64(time.Minute)) + 1 },
		"运行超时": func(s *experimentSettings) { s.RunTimeoutMinutes = int(math.MaxInt64/int64(time.Minute)) + 1 },
		"验证保留": func(s *experimentSettings) { s.VerifyHoldMinutes = int(math.MaxInt64/int64(time.Minute)) + 1 },
		"冷却重置": func(s *experimentSettings) { s.CooldownResetHours = int(math.MaxInt64/int64(time.Hour)) + 1 },
		"拒绝暂停": func(s *experimentSettings) { s.DenyHours = int(math.MaxInt64/int64(time.Hour)) + 1 },
		"间隔":   func(s *experimentSettings) { s.AccountGapSeconds = int(math.MaxInt64/int64(time.Second)) + 1 },
		"间隔相加": func(s *experimentSettings) { s.GapJitterSeconds = math.MaxInt },
		"等候秒":  func(s *experimentSettings) { s.HomeSeconds.Max = 1e20 },
		"打字毫秒": func(s *experimentSettings) { s.TypingMS.Max = 1e20 },
		"非数":   func(s *experimentSettings) { s.TypingMS.Min = math.NaN() },
	} {
		t.Run(name, func(t *testing.T) {
			s := testSettings()
			change(&s)
			if err := s.validate(); err == nil {
				t.Fatal("溢出配置被接受")
			}
		})
	}
}

func TestSettingsAcceptsRepresentableDurationBoundaries(t *testing.T) {
	s := testSettings()
	s.AccountGapSeconds = 1
	s.GapJitterSeconds = int(math.MaxInt64/int64(time.Second)) - 1
	s.RunTimeoutMinutes = int(math.MaxInt64 / int64(time.Minute))
	s.VerifyHoldMinutes = s.RunTimeoutMinutes
	s.CooldownBaseMinutes, s.CooldownMaxMinutes = s.RunTimeoutMinutes, s.RunTimeoutMinutes
	s.CooldownResetHours = int(math.MaxInt64 / int64(time.Hour))
	s.DenyHours = s.CooldownResetHours
	s.HomeSeconds.Max = math.Nextafter(float64(math.MaxInt64)/float64(time.Second), 0)
	s.TypingMS.Max = math.Nextafter(float64(math.MaxInt64)/float64(time.Millisecond), 0)
	s.BrowserCacheMB = int(math.MaxInt64 >> 20)
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	if s.HomeSeconds.pace(time.Second).Max <= 0 || s.TypingMS.pace(time.Millisecond).Max <= 0 {
		t.Fatal("可表示的浮点等待值被转成了负数")
	}
}

func TestJournalRecoveryInstructionsNameBothDirectories(t *testing.T) {
	j := testJournal(t, testSettings())
	message := j.broken().Error()
	for _, part := range []string{filepath.Dir(j.path), j.profile, "目录", "重新扫码"} {
		if !strings.Contains(message, part) {
			t.Fatalf("恢复说明缺少 %s：%s", part, message)
		}
	}
}

func TestJournalFinishedSessionDoesNotRestartGapOnRecovery(t *testing.T) {
	s := testSettings()
	j := testJournal(t, s)
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.begin(true, 0); err != nil {
		t.Fatal(err)
	}
	if err := j.finish(); err != nil {
		t.Fatal(err)
	}
	endedAt := j.state.EndedAt
	if j.state.InProgress {
		t.Fatal("正常结束没有清除会话标志")
	}
	j.unlock()
	now = now.Add(time.Hour)
	next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	defer next.unlock()
	if !next.state.EndedAt.Equal(endedAt) {
		t.Fatal("已正常结束的会话被当成崩溃恢复")
	}
	if err = next.begin(true, 0); err != nil {
		t.Fatalf("间隔已过，应允许新会话：%v", err)
	}
}

func TestJournalRecoveredGapSurvivesAnotherRestart(t *testing.T) {
	s := testSettings()
	j := testJournal(t, s)
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.begin(true, 0); err != nil {
		t.Fatal(err)
	}
	j.unlock()
	now = now.Add(time.Hour)
	next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	recoveredAt := next.state.EndedAt
	if next.state.InProgress || next.state.GapSeconds < s.AccountGapSeconds {
		t.Fatal("崩溃恢复没有保存最小间隔")
	}
	next.unlock()
	now = now.Add(time.Second)
	again, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	defer again.unlock()
	if !again.state.EndedAt.Equal(recoveredAt) {
		t.Fatal("重复重启改变了上次恢复的间隔起点")
	}
	if err = again.begin(true, 0); err == nil {
		t.Fatal("重复重启清掉了恢复间隔")
	}
	now = recoveredAt.Add(time.Duration(s.AccountGapSeconds) * time.Second)
	if err = again.begin(true, 0); err != nil {
		t.Fatalf("恢复间隔结束应可开始：%v", err)
	}
}

func TestJournalManualCooldownAllowanceSurvivesRestart(t *testing.T) {
	s := testSettings()
	j := testJournal(t, s)
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
		t.Fatal(err)
	}
	if err := j.verified(); err != nil {
		t.Fatal(err)
	}
	if err := j.begin(false, 1); err != nil {
		t.Fatal(err)
	}
	if err := j.finish(); err != nil {
		t.Fatal(err)
	}
	j.unlock()
	now = now.Add(10 * time.Minute)
	next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	defer next.unlock()
	if err = next.begin(false, 1); err == nil || !strings.Contains(err.Error(), "手动运行已使用") {
		t.Fatalf("重启恢复了冷却期的一次手动运行：%v", err)
	}
}

func TestJournalDeniedPersistsAndManualVerificationBypassesOnlyQuota(t *testing.T) {
	for _, risk := range []error{web.ErrDenied, web.ErrEnvBlocked} {
		t.Run(risk.Error(), func(t *testing.T) {
			s := testSettings()
			s.HourlyVisits = 1
			j := testJournal(t, s)
			now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
			j.now = func() time.Time { return now }
			if err := j.Acquire(quota.Search, "搜索"); err != nil {
				t.Fatal(err)
			}
			if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
				t.Fatal(err)
			}
			if err := j.begin(true, 0); err != nil {
				t.Fatalf("验证会话应能在额度耗尽时开始：%v", err)
			}
			if err := j.acquire(quota.Search, "人工验证", true); err != nil {
				t.Fatal(err)
			}
			if len(j.state.Visits) != 2 {
				t.Fatal("人工访问没有照常计数")
			}
			if err := j.risk(risk, ""); err != nil {
				t.Fatal(err)
			}
			if err := j.finish(); err != nil {
				t.Fatal(err)
			}
			until := j.state.DeniedUntil
			j.unlock()
			now = now.Add(time.Hour)
			next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
			if err != nil {
				t.Fatal(err)
			}
			defer next.unlock()
			for _, login := range []bool{false, true} {
				if err = next.begin(login, 0); err == nil || !strings.Contains(err.Error(), "被拒绝") {
					t.Fatalf("拒绝暂停没有保护会话 %t：%v", login, err)
				}
			}
			now = until
			if err = next.begin(true, 0); err != nil {
				t.Fatalf("拒绝暂停到期后应允许人工验证：%v", err)
			}
		})
	}
}

func TestJournalVerificationInterruptAndCooldownCap(t *testing.T) {
	s := testSettings()
	s.CooldownMaxMinutes = 300
	j := testJournal(t, s)
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	for i, duration := range []time.Duration{2 * time.Hour, 4 * time.Hour, 5 * time.Hour, 5 * time.Hour} {
		if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
			t.Fatal(err)
		}
		if i > 0 && (!j.state.LastCooldownEnd.Equal(now) || !j.state.CooldownUntil.IsZero()) {
			t.Fatal("验证打断冷却时没有记录实际结束时间")
		}
		if err := j.verified(); err != nil {
			t.Fatal(err)
		}
		if got := j.state.CooldownUntil.Sub(now); got != duration {
			t.Fatalf("第 %d 次冷却 %s，应为 %s", i+1, got, duration)
		}
		now = now.Add(time.Minute)
	}
	end := j.state.CooldownUntil
	now = end.Add(time.Duration(s.CooldownResetHours) * time.Hour)
	if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
		t.Fatal(err)
	}
	if j.state.VerifyLevel != 1 || !j.state.LastCooldownEnd.Equal(end) {
		t.Fatal("冷却结束一天后没有恢复首次验证等级")
	}
}

func TestJournalLargeCooldownSaturates(t *testing.T) {
	s := testSettings()
	s.CooldownBaseMinutes = 1
	s.CooldownMaxMinutes = int(math.MaxInt64 / int64(time.Minute))
	j := testJournal(t, s)
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	j.state.VerifyLevel = math.MaxInt
	j.state.LastCooldownEnd = now
	if err := j.risk(web.ErrNeedVerify, "http://local/punish"); err != nil {
		t.Fatal(err)
	}
	if j.state.VerifyLevel != math.MaxInt {
		t.Fatal("连续验证等级发生整数溢出")
	}
	if err := j.verified(); err != nil {
		t.Fatal(err)
	}
	if got := j.state.CooldownUntil.Sub(now); got != time.Duration(s.CooldownMaxMinutes)*time.Minute || got <= 0 {
		t.Fatalf("大冷却时长没有正数封顶：%s", got)
	}
}

func TestJournalRepeatedTimezoneChangesKeepAllWindowsThenPrune(t *testing.T) {
	s := testSettings()
	s.DailySearchVisits = 1
	j := testJournal(t, s)
	now := time.Date(2035, 11, 10, 10, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.Acquire(quota.Search, "搜索"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Hour)
	for _, zone := range []string{"Asia/Shanghai", "UTC", "Asia/Shanghai", "America/Los_Angeles"} {
		j.unlock()
		s.Timezone = zone
		next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { next.unlock() })
		j = next
		if err = j.Acquire(quota.Search, "搜索"); !errors.Is(err, web.ErrQuota) {
			t.Fatalf("连续切到 %s 释放了旧日额度：%v", zone, err)
		}
	}
	if len(j.state.Windows) != 2 {
		t.Fatalf("同一旧窗口重复累积：%+v", j.state.Windows)
	}
	now = time.Date(2035, 11, 11, 0, 0, 0, 0, time.UTC)
	if err := j.Acquire(quota.Search, "搜索"); !errors.Is(err, web.ErrQuota) {
		t.Fatalf("较早的旧窗口结束时仍应保留其他有效旧窗：%v", err)
	}
	if len(j.state.Windows) != 1 || j.state.Windows[0].Timezone != "Asia/Shanghai" {
		t.Fatalf("没有只清除已结束的窗口：%+v", j.state.Windows)
	}
	now = time.Date(2035, 11, 11, 16, 0, 0, 0, time.UTC)
	if err := j.Acquire(quota.Search, "搜索"); err != nil {
		t.Fatalf("全部旧日窗口结束后应恢复：%v", err)
	}
	if len(j.state.Windows) != 0 {
		t.Fatal("过期窗口没有清除")
	}
	j.unlock()
	next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	defer next.unlock()
	if len(next.state.Windows) != 0 {
		t.Fatal("过期窗口的清理没有落盘")
	}
}

func TestJournalBlockedAttemptStillPersistsHighestClock(t *testing.T) {
	s := testSettings()
	s.HourlyVisits = 1
	j := testJournal(t, s)
	now := time.Date(2035, 11, 10, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	if err := j.Acquire(quota.Search, "搜索"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	if err := j.begin(false, 1); err == nil {
		t.Fatal("额度不足仍能开始")
	}
	highest := now
	j.unlock()
	now = now.Add(-2 * time.Hour)
	next, err := openJournalAt(filepath.Dir(j.path), j.profile, s, j.now)
	if err != nil {
		t.Fatal(err)
	}
	defer next.unlock()
	if !next.effectiveNow().Equal(highest) {
		t.Fatal("未获准运行时观察的时间没有持久化")
	}
	if err = next.Acquire(quota.Detail, "详情"); !errors.Is(err, web.ErrQuota) {
		t.Fatalf("倒退时钟恢复了滚动额度：%v", err)
	}
}

func TestJournalCorruptFieldsAndExistingEmptyDirectoryFailClosed(t *testing.T) {
	for name, damage := range map[string]func(map[string]any){
		"缺数组": func(m map[string]any) { delete(m, "visits") },
		"空标志": func(m map[string]any) { m["in_progress"] = nil },
		"坏时区": func(m map[string]any) { m["timezone"] = "bad/zone" },
		"坏访问": func(m map[string]any) {
			m["visits"] = []any{map[string]any{"at": "2035-11-10T12:00:00Z", "kind": "unknown"}}
		},
		"缺访问时间": func(m map[string]any) { m["visits"] = []any{map[string]any{"kind": "search"}} },
		"缺窗口起点": func(m map[string]any) {
			m["windows"] = []any{map[string]any{"timezone": "UTC", "end": "2035-11-11T00:00:00Z"}}
		},
		"负间隔":  func(m map[string]any) { m["gap_seconds"] = -1 },
		"负等级":  func(m map[string]any) { m["verify_level"] = -1 },
		"额外字段": func(m map[string]any) { m["unexpected"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			j := testJournal(t, testSettings())
			j.unlock()
			b, err := os.ReadFile(j.path)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err = json.Unmarshal(b, &fields); err != nil {
				t.Fatal(err)
			}
			damage(fields)
			b, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(j.path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if next, err := openJournal(filepath.Dir(j.path), j.profile, testSettings()); err == nil {
				next.unlock()
				t.Fatal("损坏字段被接受")
			}
		})
	}
	base := t.TempDir()
	dir := filepath.Join(base, "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if j, err := openJournal(dir, filepath.Join(base, "profile"), testSettings()); err == nil {
		j.unlock()
		t.Fatal("已有空记录目录不能视为新账号")
	}
}

func TestJournalAtomicWritePreservesFileAndRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	if err := atomicWrite(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatalf("原子覆盖没有保存完整内容：%q %v", b, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("访问记录权限应为 0600")
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(blocked, []byte("cannot replace directory")); err == nil {
		t.Fatal("覆盖目录应失败")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatal("写入失败损坏了已有记录")
	}
	files, err := filepath.Glob(filepath.Join(dir, ".experiment-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("失败后留下临时文件：%v %v", files, err)
	}
}
