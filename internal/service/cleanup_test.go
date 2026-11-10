package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func cleanupDB(t *testing.T) (*sql.DB, settings.Values, int64) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	prefs, err := settings.Resolve(nil, settings.Environment{TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	return db.DB(), prefs.Values, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
}

func cleanupExec(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("准备本地清理数据失败：%v", err)
	}
}

func cleanupTask(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	cleanupExec(t, db, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(?,?,'camera',1,30,'keyword','recommended',?)`, id, "task"+strconv.FormatInt(id, 10), id)
}

func cleanupListing(t *testing.T, db *sql.DB, id string, seen int64, favorite, dismissed bool) {
	t.Helper()
	cleanupExec(t, db, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,is_favorite,is_dismissed) VALUES(?,?,?,1,?,?,?)`, id, id, "https://example.invalid/item/"+id, seen, favorite, dismissed)
}

func cleanupFinding(t *testing.T, db *sql.DB, id, task int64, item string, found int64) {
	t.Helper()
	cleanupExec(t, db, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read) VALUES(?,?,?,1,?,0,'matched','done',0)`, id, task, item, found)
}

func cleanupHas(t *testing.T, db *sql.DB, query string, args ...any) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestCleanupRetentionPreservesReferencesAndActiveWork(t *testing.T) {
	db, values, now := cleanupDB(t)
	old := now - 40*24*time.Hour.Milliseconds()
	cleanupTask(t, db, 1)
	cleanupTask(t, db, 2)
	for _, item := range []struct {
		id                  string
		favorite, dismissed bool
	}{
		{"orphan", false, false}, {"favorite", true, false}, {"dismissed", false, true},
		{"expired", false, false}, {"fresh", false, false}, {"active", false, false},
		{"old-sighting", false, false}, {"fresh-sighting", false, false},
	} {
		cleanupListing(t, db, item.id, old, item.favorite, item.dismissed)
		cleanupExec(t, db, `INSERT INTO price_points(item_id,price,observed_at) VALUES(?,100,?)`, item.id, old)
	}
	cleanupFinding(t, db, 1, 1, "expired", old)
	cleanupFinding(t, db, 2, 1, "favorite", old)
	cleanupFinding(t, db, 3, 1, "fresh", now)
	cleanupFinding(t, db, 4, 2, "active", old)
	cleanupExec(t, db, `UPDATE listings SET seller_id='active-seller' WHERE item_id='active'`)
	cleanupExec(t, db, `INSERT INTO seen_items(task_id,item_id,first_seen_at) VALUES(1,'expired',?)`, old)
	cleanupExec(t, db, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(1,'old-sighting','2026-09-01',100,?),(1,'fresh-sighting','2026-11-11',100,?)`, old, now)
	cleanupExec(t, db, `INSERT INTO task_item_specs(task_id,item_id,source,updated_at) VALUES(1,'orphan-spec','user',?),(1,'fresh-sighting','user',?)`, now, now)
	cleanupExec(t, db, `INSERT INTO seller_profiles(seller_id,fetched_at) VALUES('expired-seller',?),('active-seller',?)`, old, old)
	cleanupExec(t, db, `INSERT INTO market_insights(id,task_id,generated_at) VALUES(1,1,?),(2,1,?),(3,1,?)`, old-1, old, old)
	cleanupExec(t, db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,ended_at,outcome) VALUES(1,1,'old','auto',?,?,'success'),(2,2,'active','auto',?,?,'failed'),(3,1,'unfinished','auto',?,NULL,'failed'),(4,1,'fresh','auto',?,?,'success')`, old, old, old, old, old, now, now)
	cleanupExec(t, db, `INSERT INTO run_steps(run_id,seq,at,status) VALUES(1,1,?,'ok'),(2,1,?,'fail'),(3,1,?,'ok'),(4,1,?,'ok')`, old, old, old, old)
	cleanupExec(t, db, `INSERT INTO ai_calls(id,at,task_id) VALUES(1,?,1),(2,?,2),(3,?,1)`, old, old, now)
	cleanupExec(t, db, `INSERT INTO deliveries(id,at,task_id) VALUES(1,?,1),(2,?,2),(3,?,1)`, old, old, now)
	cleanupExec(t, db, `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(1,1),(2,4)`)
	cleanupExec(t, db, `INSERT INTO accounts(id,name,state) VALUES(1,'protected','cooling')`)
	cleanupExec(t, db, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',?),(1,'search',?),(1,'detail',?)`, now-48*time.Hour.Milliseconds()-1, now-48*time.Hour.Milliseconds(), now)
	cleanupExec(t, db, `INSERT INTO system_events(id,kind,subject_id,created_at,resolved_at) VALUES(1,'xianyu_layout',0,?,?),(2,'account_deny',1,?,NULL),(3,'task_failure_pause',1,?,?)`, old, old, old, old, now)

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := cleanupSQLTx(t.Context(), tx, values, now, []int64{2}, []int64{2}, now-24*time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"seen_items", "tasks", "accounts"} {
		if result.Deleted[table] != 0 {
			t.Fatalf("保护表%s被清理：%+v", table, result.Deleted)
		}
	}
	for item, want := range map[string]bool{"orphan": false, "expired": false, "old-sighting": false, "favorite": true, "dismissed": true, "fresh": true, "active": true, "fresh-sighting": true} {
		if got := cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM listings WHERE item_id=?)`, item); got != want {
			t.Errorf("商品%s是否保留=%v，要%v", item, got, want)
		}
	}
	for table, want := range map[string]int64{"findings": 1, "listings": 3, "price_points": 3, "task_sightings": 1, "task_item_specs": 1, "seller_profiles": 1, "market_insights": 2, "run_records": 1, "run_steps": 2, "ai_calls": 1, "deliveries": 1, "delivery_items": 1, "quota_events": 1, "system_events": 1} {
		if result.Deleted[table] != want {
			t.Errorf("%s实际删除计数=%d，要%d", table, result.Deleted[table], want)
		}
	}
	if !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM market_insights WHERE id=3)`) || !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM run_steps WHERE run_id=2)`) || !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM run_steps WHERE run_id=3)`) {
		t.Fatal("最新行情或活跃步骤没有保留")
	}
}

func TestCleanupPermanentFindingsAndExpiredTokens(t *testing.T) {
	db, values, now := cleanupDB(t)
	values["retention.findings_days"] = json.RawMessage(` 0 `)
	old := now - 200*24*time.Hour.Milliseconds()
	cleanupTask(t, db, 1)
	cleanupListing(t, db, "permanent", old, false, false)
	cleanupFinding(t, db, 1, 1, "permanent", old)
	for _, row := range []struct {
		id, created int64
		kind        string
		expires     any
		revoked     any
	}{
		{1, old, "api_token", nil, nil},
		{2, old, "api_token", nil, now - 31*24*time.Hour.Milliseconds()},
		{3, now - 31*24*time.Hour.Milliseconds(), "web_session", now - 31*24*time.Hour.Milliseconds(), nil},
		{4, now - 121*24*time.Hour.Milliseconds(), "web_session", now + 30*24*time.Hour.Milliseconds(), nil},
		{5, now - 100*24*time.Hour.Milliseconds(), "web_session", now + 30*24*time.Hour.Milliseconds(), nil},
		{6, old, "api_token", nil, now},
	} {
		hash := make([]byte, 32)
		hash[0] = byte(row.id)
		cleanupExec(t, db, `INSERT INTO auth_tokens(id,kind,token_hash,name,created_at,last_used_at,expires_at,revoked_at) VALUES(?,?,?,'token',?,?,?,?)`, row.id, row.kind, hash, row.created, row.created, row.expires, row.revoked)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := cleanupSQLTx(t.Context(), tx, values, now, nil, nil, now-24*time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if result.Deleted["findings"] != 0 || result.Deleted["auth_tokens"] != 3 {
		t.Fatalf("永久发现或令牌期限错误：%+v", result.Deleted)
	}
	for _, id := range []int64{1, 5, 6} {
		if !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM auth_tokens WHERE id=?)`, id) {
			t.Errorf("未超过清理期限的令牌%d被删", id)
		}
	}
}

func TestCleanupFailureCanRollbackAllDeletesAndCoverage(t *testing.T) {
	db, values, now := cleanupDB(t)
	old := now - 40*24*time.Hour.Milliseconds()
	cleanupTask(t, db, 1)
	cleanupListing(t, db, "failure", old, false, false)
	cleanupFinding(t, db, 1, 1, "failure", old)
	cleanupExec(t, db, `CREATE TRIGGER stop_cleanup BEFORE DELETE ON listings BEGIN SELECT RAISE(ABORT,'local failure'); END`)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cleanupSQLTx(t.Context(), tx, values, now, nil, nil, now-24*time.Hour.Milliseconds()); err == nil {
		t.Fatal("清理失败被吞掉")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM findings WHERE id=1)`) || cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM meta WHERE key='stats.coverage.findings')`) {
		t.Fatal("清理错误留下部分删除或覆盖证明")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := cleanupSQLTx(cancelled, tx, values, now, nil, nil, now-24*time.Hour.Milliseconds()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消原因被吞：%v", err)
	}
}

func TestStatsCoverageOnlyAdvancesAndRejectsCorruption(t *testing.T) {
	db, _, now := cleanupDB(t)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, cutoff := range []int64{now - 1000, now - 2000, now} {
		if err := raiseStatsCoverageTx(t.Context(), tx, "findings", cutoff); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err := tx.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='stats.coverage.findings'`).Scan(&raw); err != nil || raw != strconv.FormatInt(now, 10) {
		t.Fatalf("覆盖证明倒退：%s %v", raw, err)
	}
	cleanupExec(t, tx, `UPDATE meta SET value='broken' WHERE key='stats.coverage.findings'`)
	if err := raiseStatsCoverageTx(t.Context(), tx, "findings", now); err == nil {
		t.Fatal("损坏覆盖证明被静默重建")
	}
}

func TestCleanupSettlesExpiredFindingIntentAndCountsCascades(t *testing.T) {
	db, values, now := cleanupDB(t)
	old := now - 40*24*time.Hour.Milliseconds()
	cleanupTask(t, db, 1)
	cleanupTask(t, db, 2)
	cleanupListing(t, db, "expired", old, false, false)
	cleanupListing(t, db, "favorite", old, true, false)
	cleanupListing(t, db, "active", old, false, false)
	cleanupFinding(t, db, 1, 1, "expired", old)
	cleanupFinding(t, db, 2, 1, "favorite", old)
	cleanupFinding(t, db, 4, 2, "active", old)
	cleanupExec(t, db, `INSERT INTO notification_intents(id,finding_id,task_id,kind,state,created_at,updated_at) VALUES(1,1,1,'recommended','pending',?,?),(2,2,1,'recommended','pending',?,?),(3,3,1,'price_drop','sent',?,?),(4,4,2,'price_drop','sent',?,?)`, old, old, old, old, old, old, old, old)
	cleanupExec(t, db, `INSERT INTO deliveries(id,at) VALUES(1,?)`, now)
	cleanupExec(t, db, `INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(1,3)`)
	cleanupExec(t, db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,ended_at,outcome) VALUES(1,1,'old','auto',?,?,'skipped')`, old, old)
	cleanupExec(t, db, `INSERT INTO run_skip_events(run_id,seq,at) VALUES(1,1,?)`, old)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := cleanupSQLTx(t.Context(), tx, values, now, []int64{2}, nil, now-24*time.Hour.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM notification_intents WHERE id=1 AND state='cancelled' AND updated_at=? AND json_extract(reason,'$.code')='maintenance.finding_expired')`, now) {
		t.Fatal("已过期发现的待发意图未持久取消")
	}
	if !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM notification_intents WHERE id=2 AND state='pending')`) || !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM notification_intents WHERE id=4)`) {
		t.Fatal("收藏或活跃任务的意图被清理")
	}
	if result.Deleted["notification_intents"] != 1 || result.Deleted["delivery_intents"] != 1 || result.Deleted["run_skip_events"] != 1 {
		t.Fatalf("新表或级联计数错误：%+v", result.Deleted)
	}
}

func TestCleanupDoesNotReleaseProtectedAIDayBudget(t *testing.T) {
	db, values, now := cleanupDB(t)
	values["retention.stats_days"] = json.RawMessage(`1`)
	protectedStart := now - 26*time.Hour.Milliseconds()
	cleanupExec(t, db, `INSERT INTO ai_calls(id,at,input_tokens,output_tokens) VALUES(1,?,100,50),(2,?,200,50),(3,?,300,50)`, protectedStart-1, protectedStart, now-25*time.Hour.Milliseconds())
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := cleanupSQLTx(t.Context(), tx, values, now, nil, nil, protectedStart)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if result.Deleted["ai_calls"] != 1 || !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM ai_calls WHERE id=2)`) || !cleanupHas(t, db, `SELECT EXISTS(SELECT 1 FROM ai_calls WHERE id=3)`) {
		t.Fatal("清理恢复了保护中的当日模型用量")
	}
	var proof string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='stats.coverage.ai_calls'`).Scan(&proof); err != nil || proof != strconv.FormatInt(protectedStart, 10) {
		t.Fatalf("用量覆盖证明没有对应实际保留边界：%s %v", proof, err)
	}
}
