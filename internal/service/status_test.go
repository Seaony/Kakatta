package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func statusTestService(t *testing.T) (*Service, time.Time) {
	t.Helper()
	directory := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(directory, "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db.DB(), settings: preferences, dataDir: directory, now: func() time.Time { return now }}
	return s, now
}

func TestSystemStatusUsesProtectedClockAndActualSessions(t *testing.T) {
	s, now := statusTestService(t)
	if s.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("测试必须使用正式单连接设置")
	}
	for _, statement := range []string{
		`INSERT INTO accounts(id,name,state) VALUES(1,'未登录账号','signed_out'),(2,'可用账号','active')`,
	} {
		if _, err := s.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []struct {
		account int
		kind    string
		at      time.Time
	}{
		{1, "search", now.Add(-10 * time.Minute)}, {1, "detail", now.Add(-20 * time.Minute)},
		{1, "search", now.Add(-2 * time.Hour)}, {2, "detail", now.Add(-5 * time.Minute)},
	} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO quota_events(account_id,kind,at) VALUES(?,?,?)`, event.account, event.kind, event.at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.settings.Apply(t.Context(), settings.Values{"ai.image_count": json.RawMessage(`5`)}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(-time.Hour) }
	s.initRuntime()
	s.mu.Lock()
	s.pageLimit = 8
	s.sessions["opening"] = &sessionWork{View: SessionView{ID: "opening", AccountID: 1, Kind: SessionLogin, Phase: SessionOpening}}
	s.sessions["closing"] = &sessionWork{View: SessionView{ID: "closing", AccountID: 2, Kind: SessionRun, Phase: SessionClosing}}
	s.sessions["queued"] = &sessionWork{View: SessionView{ID: "queued", AccountID: 3, Kind: SessionLogin, Phase: SessionQueued}}
	s.queue = []*startRequest{{AccountID: 3, Kind: SessionLogin, SessionID: "queued"}}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	view, err := s.GetSystemStatus(ctx)
	if err != nil {
		t.Fatal("读取发生了自等或丢失保护时间", err)
	}
	if view.AsOf != now.UnixMilli() || view.Browsers.Limit != 1 || view.Browsers.Active != 2 || len(view.Browsers.Sessions) != 3 || view.Scheduler.QueuedCount != 1 {
		t.Fatalf("没有使用权威运行状态：%+v", view)
	}
	if len(view.Quotas) != 2 || view.Quotas[0].AccountID != 1 || view.Quotas[0].Quota.HourlyUsed != 2 || view.Quotas[0].Quota.DailySearchUsed != 2 || view.Quotas[0].Quota.DailyDetailUsed != 1 || view.Quotas[1].Quota.DailyDetailUsed != 1 {
		t.Fatalf("账号状态被误用来过滤额度或统计不准：%+v", view.Quotas)
	}
	if len(view.Configuration) != len(s.settings.Definitions()) {
		t.Fatal("配置检查没有覆盖全部定义")
	}
	foundWarning := false
	for _, check := range view.Configuration {
		if !check.OK {
			t.Fatalf("合法的推荐范围外配置被当成非法：%+v", check)
		}
		if check.Key == "ai.image_count" {
			foundWarning = check.Reason != nil && check.Reason.Code == "settings.outside_recommendation"
		}
	}
	if !foundWarning {
		t.Fatal("推荐范围警告被丢掉")
	}
	if view.Resources.DiskTotalBytes == nil || view.Resources.DiskFreeBytes == nil || *view.Resources.DiskTotalBytes <= 0 {
		t.Fatal("没有读取数据目录所在磁盘")
	}
	var raw string
	if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var clock quota.ClockState
	if json.Unmarshal([]byte(raw), &clock) != nil || clock.HighestUTC != view.AsOf {
		t.Fatal("状态读取没有沿用持久化的最高观察时间")
	}
	view.Browsers.Sessions[0].ID = "被调用方改写"
	again, err := s.GetSystemStatus(ctx)
	if err != nil || again.Browsers.Sessions[0].ID == "被调用方改写" {
		t.Fatal("状态对象污染了运行时", err)
	}
}

func TestSystemStatusStorageErrorsFailClosedWithoutRepairingClock(t *testing.T) {
	for _, test := range []string{"clock_missing", "clock_corrupt", "clock_marker_missing", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			s, _ := statusTestService(t)
			var previous string
			if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&previous); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch test {
			case "clock_missing":
				if _, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, appsettings.ClockKey); err != nil {
					t.Fatal(err)
				}
			case "clock_corrupt":
				if _, err := s.db.ExecContext(ctx, `UPDATE meta SET value='{}' WHERE key=?`, appsettings.ClockKey); err != nil {
					t.Fatal(err)
				}
				previous = "{}"
			case "clock_marker_missing":
				if _, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key='quota.clock_initialized'`); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			_, err := s.GetSystemStatus(ctx)
			if test == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("取消原因丢失：%v", err)
				}
			} else {
				var failure *ServiceError
				if !errors.As(err, &failure) || failure.Kind != ErrorUnavailable || failure.Reason.Code != "service.storage_failure" {
					t.Fatalf("存储失败被当成成功或泄露细节：%v", err)
				}
			}
			if test != "clock_missing" {
				var after string
				if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&after); err != nil || after != previous {
					t.Fatal("失败的状态读取没有回滚时钟", err)
				}
			}
		})
	}
}

func TestSystemStatusAdvancesQuotaOnlyInMemory(t *testing.T) {
	s, now := statusTestService(t)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO accounts(id,name,state) VALUES(1,'只读测试','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',?)`, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER status_clock_read_only BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'status must not write'); END`); err != nil {
		t.Fatal(err)
	}
	next := now.Add(24 * time.Hour)
	s.now = func() time.Time { return next }
	view, err := s.GetSystemStatus(t.Context())
	if err != nil {
		t.Fatal("状态查询不应写回时钟", err)
	}
	if view.AsOf != next.UnixMilli() || len(view.Quotas) != 1 || view.Quotas[0].Quota.HourlyUsed != 0 || view.Quotas[0].Quota.DailySearchUsed != 0 || view.Quotas[0].Quota.DayStartAt != next.Truncate(24*time.Hour).UnixMilli() {
		t.Fatalf("只读查询没有按真实当前日界计算：%+v", view)
	}
	var after string
	if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&after); err != nil || after != before {
		t.Fatal("只读查询修改了持久时钟", err)
	}
}

func TestSystemStatusEmptyArraysAndUnknownDisk(t *testing.T) {
	s, _ := statusTestService(t)
	s.dataDir = filepath.Join(t.TempDir(), "does-not-exist")
	view, err := s.GetSystemStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Resources.DiskTotalBytes != nil || view.Resources.DiskFreeBytes != nil {
		t.Fatal("磁盘读取失败被伪造为0")
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || string(object["quotas"]) != "[]" {
		t.Fatal("空额度列表不是数组", string(raw))
	}
	if len(view.Browsers.Sessions) != 0 || view.Browsers.Sessions == nil {
		t.Fatal("空会话列表不是数组")
	}
}

func TestStatusDiskBytesChecksOverflowAndKeepsKnownZero(t *testing.T) {
	for _, test := range []struct {
		blocks, blockSize uint64
		want              *int64
	}{
		{0, 4096, systemStatusTestNumber(0)}, {10, 4096, systemStatusTestNumber(40960)},
		{1, 0, nil}, {math.MaxUint64, 4096, nil}, {2, math.MaxUint64, nil},
	} {
		if got := statusDiskBytes(test.blocks, test.blockSize); !sameSystemStatusTestNumber(got, test.want) {
			t.Fatalf("磁盘字节换算错误：%d * %d = %v", test.blocks, test.blockSize, got)
		}
	}
}

func systemStatusTestNumber(value int64) *int64 { return &value }
func sameSystemStatusTestNumber(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
