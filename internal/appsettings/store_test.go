package appsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func testStore(t *testing.T, env settings.Environment, now func() time.Time) (*Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New(t.Context(), db.DB(), env, now)
	if err != nil {
		t.Fatal(err)
	}
	return s, db.DB()
}

func clockState(t *testing.T, db *sql.DB) quota.ClockState {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, ClockKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state quota.ClockState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSettingsPersistAndAtomicValidation(t *testing.T) {
	s, db := testStore(t, settings.Environment{TZ: "UTC"}, nil)
	patch := settings.Values{"ai.image_count": json.RawMessage(`8`), "general.language": json.RawMessage(`"en"`)}
	snapshot, err := s.Apply(t.Context(), patch)
	if err != nil || snapshot.Sources["ai.image_count"] != settings.SourceStored || len(snapshot.Warnings) == 0 {
		t.Fatalf("推荐外合法值未保存：%+v %v", snapshot, err)
	}
	patch["ai.image_count"][0] = '1'
	got, err := s.Get(t.Context())
	if err != nil || string(got.Values["ai.image_count"]) != "8" {
		t.Fatalf("调用方修改污染了保存值：%v", err)
	}
	_, err = s.Apply(t.Context(), settings.Values{
		"ai.image_count": json.RawMessage(`0`), "general.language": json.RawMessage(`"zh-CN"`),
	})
	var invalid *settings.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("未拒绝非法设置：%v", err)
	}
	got, _ = s.Get(t.Context())
	if string(got.Values["general.language"]) != `"en"` {
		t.Fatal("校验失败仍保存了部分设置")
	}
	if _, err := New(t.Context(), db, settings.Environment{TZ: "Asia/Shanghai"}, nil); err != nil {
		t.Fatal(err)
	}
	if clockState(t, db).Timezone != "Asia/Shanghai" {
		t.Fatal("默认时区变化未同步额度时钟")
	}
}

func TestTimezoneClockRollbackAndSaveFailure(t *testing.T) {
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	s, db := testStore(t, settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if _, err := s.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"Asia/Shanghai"`)}); err != nil {
		t.Fatal(err)
	}
	state := clockState(t, db)
	if len(state.Windows) != 1 || state.HighestUTC != now.UnixMilli() {
		t.Fatalf("未记旧时区窗口：%+v", state)
	}
	now = now.Add(-24 * time.Hour)
	if _, err := s.Get(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, clockState(t, db)) {
		t.Fatal("回调系统时钟恢复了额度")
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"America/New_York"`)}); err == nil {
		t.Fatal("保存失败却返回成功")
	}
	var timezone string
	if err := db.QueryRow(`SELECT value_json FROM settings WHERE key='general.timezone'`).Scan(&timezone); err != nil || timezone != `"Asia/Shanghai"` {
		t.Fatalf("写失败仍更改了设置：%s %v", timezone, err)
	}
	if !reflect.DeepEqual(state, clockState(t, db)) {
		t.Fatal("写失败仍更改了额度窗口")
	}
}

func TestMissingOrBrokenClockIsNeverRecreated(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM meta WHERE key='quota.clock'`,
		`UPDATE meta SET value='{}' WHERE key='quota.clock'`,
		`UPDATE meta SET value='{"version":1,"timezone":"UTC","windows":[]}' WHERE key='quota.clock'`,
		`DELETE FROM meta WHERE key='quota.clock_initialized'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s, db := testStore(t, settings.Environment{TZ: "UTC"}, nil)
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(t.Context()); err == nil {
				t.Fatal("运行中损坏的时钟被重建")
			}
			if _, err := New(t.Context(), db, settings.Environment{TZ: "UTC"}, nil); err == nil {
				t.Fatal("重启后损坏的时钟被重建")
			}
		})
	}
}

func TestReferencesAndEnvironmentCannotBeBypassed(t *testing.T) {
	empty := ""
	s, db := testStore(t, settings.Environment{TZ: "UTC", TrustedProxies: &empty}, nil)
	for _, patch := range []settings.Values{
		{"ai.proxy_id": json.RawMessage(`9`)},
		{"notify.system_channel_ids": json.RawMessage(`[8]`)},
		{"security.trusted_proxies": json.RawMessage(`["127.0.0.0/8"]`)},
	} {
		if _, err := s.Apply(t.Context(), patch); err == nil {
			t.Fatalf("接受了无效引用或只读覆盖：%v", patch)
		}
	}
	if _, err := db.Exec(`INSERT INTO proxies(id,uuid,name,url_enc) VALUES(9,'p','本地代理',X'00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channels(id,uuid,type,name,config_json) VALUES(8,'c','webhook','本地通知','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), settings.Values{"ai.proxy_id": json.RawMessage(`9`), "notify.system_channel_ids": json.RawMessage(`[8]`)}); err != nil {
		t.Fatal(err)
	}
	empty = "127.0.0.0/8"
	got, err := s.Get(t.Context())
	if err != nil || string(got.Values["security.trusted_proxies"]) != `[]` {
		t.Fatalf("外部改动污染了启动环境：%v", err)
	}
}

func TestCanceledSettingsDoesNotWrite(t *testing.T) {
	s, db := testStore(t, settings.Environment{TZ: "UTC"}, nil)
	before := clockState(t, db)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Apply(ctx, settings.Values{"general.language": json.RawMessage(`"en"`)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, clockState(t, db)) {
		t.Fatal("取消后仍写入时钟")
	}
}

func TestGetKeepsPersistedClockAndDoesNotRequireClockWrites(t *testing.T) {
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	s, db := testStore(t, settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if _, err := s.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"Asia/Shanghai"`)}); err != nil {
		t.Fatal(err)
	}
	before := clockState(t, db)
	now = now.Add(48 * time.Hour)
	if _, err := s.Get(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, clockState(t, db)) {
		t.Fatal("读取设置推进了保护时钟或删除了旧日窗口")
	}
	if _, err := db.Exec(`CREATE TRIGGER forbid_clock_write BEFORE INSERT ON meta
		WHEN NEW.key IN ('quota.clock','quota.clock_initialized') BEGIN SELECT RAISE(FAIL,'read only'); END`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Get(t.Context())
	if err != nil || string(snapshot.Values["general.timezone"]) != `"Asia/Shanghai"` {
		t.Fatalf("读取设置依赖保护时钟可写：%v", err)
	}
}
