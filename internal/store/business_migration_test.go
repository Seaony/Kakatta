package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func versionOneDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v1.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	execute(t, db, migrationTable)
	execute(t, db, initialSchema)
	execute(t, db, `INSERT INTO schema_migrations(version,applied_at) VALUES(1,1)`)
	return path, db
}

func seedVersionOneBusiness(t *testing.T, db *sql.DB) {
	t.Helper()
	execute(t, db, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index)
		VALUES(1,'任务','相机',1,30,'keyword','recommended',0)`)
	for id := 1; id <= 5; id++ {
		item := string(rune('a' + id))
		execute(t, db, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES(?,?,?,1,1)`, item, item, item)
		execute(t, db, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read,recommended_price)
			VALUES(?,1,?,1,1,0,'matched','done',0,10000)`, id, item)
	}
	execute(t, db, `UPDATE findings SET notify_kind='recommended',notify_failures=2 WHERE id=1`)
	execute(t, db, `UPDATE findings SET notify_kind='new',notify_failures=1 WHERE id=2`)
	execute(t, db, `UPDATE findings SET notify_kind='recommended',notify_failures=1,notified_at=30 WHERE id=3`)
	execute(t, db, `UPDATE findings SET reanalyze_requested_at=8 WHERE id=4`)
	execute(t, db, `UPDATE findings SET notify_kind='recommended',notify_skip_reason='{"code":"baseline","args":{}}' WHERE id=5`)
	for _, delivery := range []struct {
		id, at, finding, ok int
		kind                string
	}{
		{1, 10, 1, 0, "recommended"}, {2, 20, 1, 0, "recommended"},
		{3, 25, 3, 0, "recommended"}, {4, 30, 3, 1, "recommended"},
		{5, 50, 3, 1, "price_drop"}, {6, 60, 3, 1, "summary"},
	} {
		execute(t, db, `INSERT INTO deliveries(id,at,task_id,kind,ok) VALUES(?,?,1,?,?)`, delivery.id, delivery.at, delivery.kind, delivery.ok)
		execute(t, db, `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(?,?)`, delivery.id, delivery.finding)
	}
	execute(t, db, `INSERT INTO run_records(id,task_id,task_name,trigger,started_at,ended_at,outcome,repeat_count) VALUES
		(1,1,'任务','auto',10,10,'skipped',1),
		(2,1,'任务','auto',20,40,'skipped',3),
		(3,1,'任务','auto',NULL,NULL,'skipped',1),
		(4,1,'任务','auto',50,NULL,'failed',1)`)
	execute(t, db, `INSERT INTO seller_profiles(seller_id,recent_rates_json,fetched_at) VALUES('unknown',NULL,1),('empty','[]',1)`)
	execute(t, db, `INSERT INTO meta(key,value) VALUES('quota.clock','{"version":1,"highest_utc":4102444800000,"timezone":"UTC","windows":[]}')`)
}

func TestBusinessUpgradePreservesNotificationFactsAndMarksCoverage(t *testing.T) {
	path, db := versionOneDB(t)
	seedVersionOneBusiness(t, db)
	db.Close()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if got := scalar[int](t, s.DB(), `SELECT max(version) FROM schema_migrations`); got != 2 {
		t.Fatal("没有升级到第二版", got)
	}
	for _, want := range []struct {
		finding, failures int
		state             string
		failed, sent      *int64
	}{
		{1, 2, "pending", int64Pointer(10), nil},
		{2, 1, "abandoned", nil, nil},
		{3, 1, "sent", int64Pointer(25), int64Pointer(30)},
		{5, 0, "cancelled", nil, nil},
	} {
		var state string
		var failures int
		var failed, sent sql.NullInt64
		if err := s.DB().QueryRow(`SELECT state,failure_count,first_failed_at,sent_at FROM notification_intents WHERE finding_id=?`, want.finding).
			Scan(&state, &failures, &failed, &sent); err != nil {
			t.Fatal(err)
		}
		if state != want.state || failures != want.failures || failed.Valid != (want.failed != nil) || sent.Valid != (want.sent != nil) ||
			want.failed != nil && failed.Int64 != *want.failed || want.sent != nil && sent.Int64 != *want.sent {
			t.Fatalf("发现%d的旧通知事实被改写：%s %d %v %v", want.finding, state, failures, failed, sent)
		}
	}
	if got := scalar[string](t, s.DB(), `SELECT json_extract(reason,'$.code') FROM notification_intents WHERE finding_id=2`); got != "notification.legacy_retry_unknown" {
		t.Fatal("缺失败时间没有明确终止原因", got)
	}
	if got := scalar[string](t, s.DB(), `SELECT json_extract(notify_skip_reason,'$.code') FROM findings WHERE id=2`); got != "notification.legacy_retry_unknown" {
		t.Fatal("发现展示没有同步结算", got)
	}
	if got := scalar[string](t, s.DB(), `SELECT reason FROM notification_intents WHERE finding_id=5`); got != `{"code":"baseline","args":{}}` {
		t.Fatal("原不推送原因丢失", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM delivery_intents`); got != 4 {
		t.Fatal("旧首次推荐关联不完整", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM delivery_intents WHERE delivery_id IN (5,6)`); got != 0 {
		t.Fatal("旧降价或合并历史被虚构为首次意图", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM deliveries`); got != 6 {
		t.Fatal("旧投递历史丢失", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM run_skip_events WHERE run_id=1 AND seq=1 AND at=10`); got != 1 {
		t.Fatal("单次跳过没有保留确切时点", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM run_skip_events WHERE run_id IN (2,3,4)`); got != 0 {
		t.Fatal("折叠或未知时点被伪造", got)
	}
	for _, key := range []string{"stats.coverage.notification_intents", "stats.coverage.run_skip_events"} {
		if got := scalar[int64](t, s.DB(), `SELECT value FROM meta WHERE key=?`, key); got != 4102444800000 {
			t.Fatal("覆盖起点没有保留最高已观察时间", key, got)
		}
	}
	if got := scalar[int](t, s.DB(), `SELECT reanalysis_origin FROM findings WHERE id=4`); got != 1 {
		t.Fatal("已有重分析请求的来源没有保存", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM pragma_table_info('findings') WHERE name='notify_failures'`); got != 0 {
		t.Fatal("旧失败计数仍有第二套真相")
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM findings WHERE processing_revision=1`); got != 5 {
		t.Fatal("旧发现没有初始化工作版本", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT recent_rates_json IS NULL FROM seller_profiles WHERE seller_id='unknown'`); got != 1 {
		t.Fatal("没取得评价被改成空评价")
	}
	if got := scalar[string](t, s.DB(), `SELECT recent_rates_json FROM seller_profiles WHERE seller_id='empty'`); got != "[]" {
		t.Fatal("取得空评价的事实丢失")
	}
	backups := ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))
	if len(backups) != 1 {
		t.Fatal("升级没有一份旧库快照", backups)
	}
	before, err := sql.Open("sqlite", dataSource(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	if scalar[int](t, before, `SELECT max(version) FROM schema_migrations`) != 1 || scalar[int](t, before, `SELECT notify_failures FROM findings WHERE id=1`) != 2 {
		t.Fatal("升级前快照不是原版本原数据")
	}
	s = reopenStore(t, s)
	if scalar[int](t, s.DB(), `SELECT failure_count FROM notification_intents WHERE finding_id=1`) != 2 || len(ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))) != 1 {
		t.Fatal("重开重复迁移或重置失败次数")
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestBusinessUpgradeRollsBackAllNewSchemaAndData(t *testing.T) {
	path, db := versionOneDB(t)
	seedVersionOneBusiness(t, db)
	db.Close()
	steps := append(append([]migration{}, migrations...), migration{version: 3, sql: `INVALID MIGRATION`})
	if s, err := open(t.Context(), path, steps); err == nil {
		s.Close()
		t.Fatal("故障迁移不能提交部分升级")
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if scalar[int](t, db, `SELECT max(version) FROM schema_migrations`) != 1 || scalar[int](t, db, `SELECT notify_failures FROM findings WHERE id=1`) != 2 {
		t.Fatal("回滚没有保留旧版本与计数")
	}
	if scalar[int](t, db, `SELECT count(*) FROM sqlite_schema WHERE name IN ('notification_intents','delivery_intents','run_skip_events')`) != 0 ||
		scalar[int](t, db, `SELECT count(*) FROM pragma_table_info('findings') WHERE name IN ('processing_revision','reanalysis_origin')`) != 0 ||
		scalar[int](t, db, `SELECT count(*) FROM meta WHERE key LIKE 'stats.coverage.%'`) != 0 {
		t.Fatal("失败后留下部分新结构或覆盖标记")
	}
}

func TestBusinessUpgradePreservesCancelledLegacyFailureCounts(t *testing.T) {
	path, db := versionOneDB(t)
	seedVersionOneBusiness(t, db)
	execute(t, db, `UPDATE findings SET notify_kind=NULL,notify_failures=2,notify_skip_reason='{"code":"search_changed","args":{}}' WHERE id IN (1,3,4)`)
	db.Close()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 只有一种明确首次提醒投递的，能证明已取消意图的种类。
	if state := scalar[string](t, s.DB(), `SELECT state FROM notification_intents WHERE finding_id=1`); state != "cancelled" ||
		scalar[int](t, s.DB(), `SELECT failure_count FROM notification_intents WHERE finding_id=1`) != 2 {
		t.Fatal("可证明的已取消提醒没有保留计数", state)
	}
	for _, id := range []int{3, 4} {
		if got := scalar[int](t, s.DB(), `SELECT count(*) FROM notification_intents WHERE finding_id=?`, id); got != 0 {
			t.Fatal("混合或缺失投递种类被猜成意图", id)
		}
		key := "migration.legacy_notify_failures." + strconv.Itoa(id)
		if got := scalar[string](t, s.DB(), `SELECT value FROM meta WHERE key=?`, key); got != "2" {
			t.Fatal("不可证明的旧计数未保真归档", key, got)
		}
		if got := scalar[string](t, s.DB(), `SELECT notify_skip_reason FROM findings WHERE id=?`, id); got != `{"code":"search_changed","args":{}}` {
			t.Fatal("旧发现终止原因被改写", id, got)
		}
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM meta WHERE key='migration.legacy_notify_failures.1'`); got != 0 {
		t.Fatal("同一旧计数不应既有意图又有档案")
	}
}

func TestBusinessSchemaConstraintsAndHistoryLinks(t *testing.T) {
	s := testStore(t)
	schemaSeed(t, s.DB())
	tx := schemaTransaction(t, s.DB())
	for _, query := range []string{
		`UPDATE findings SET processing_revision=0`, `UPDATE findings SET processing_revision=-1`,
		`UPDATE notification_intents SET failure_count=-1`, `UPDATE notification_intents SET price=-1`,
		`UPDATE notification_intents SET previous_price=-1`, `UPDATE run_skip_events SET seq=0`,
		`UPDATE run_skip_events SET at=NULL`,
		`INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at) VALUES(1,1,'new','pending',2,2)`,
		`INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(999,1)`,
		`INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(1,999)`,
	} {
		schemaReject(t, tx, query)
	}
	for _, column := range []string{"seller_good_count", "seller_rate_count", "buyer_good_count", "buyer_rate_count"} {
		for _, value := range []any{nil, 0, 10} {
			schemaExec(t, tx, `UPDATE seller_profiles SET `+column+`=?`, value)
		}
		schemaReject(t, tx, `UPDATE seller_profiles SET `+column+`=-1`)
	}
	schemaExec(t, tx, `UPDATE notification_intents SET state='sent',sent_at=2 WHERE id=1`)
	schemaExec(t, tx, `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at) VALUES(1,1,'price_drop','pending',3,3)`)
	schemaExec(t, tx, `DELETE FROM findings WHERE id=1`)
	schemaExec(t, tx, `DELETE FROM tasks WHERE id=1`)
	schemaCount(t, tx, `SELECT count(*) FROM notification_intents WHERE finding_id=1 AND task_id=1`, 2)
	schemaCount(t, tx, `SELECT count(*) FROM delivery_intents`, 1)
	schemaExec(t, tx, `DELETE FROM notification_intents WHERE id=1`)
	schemaCount(t, tx, `SELECT count(*) FROM delivery_intents`, 0)
	schemaCount(t, tx, `SELECT count(*) FROM deliveries`, 1)
	schemaCount(t, tx, `SELECT count(*) FROM delivery_items`, 1)
	schemaExec(t, tx, `DELETE FROM run_records WHERE id=1`)
	schemaCount(t, tx, `SELECT count(*) FROM run_skip_events`, 0)
}

func TestFreshBusinessDatabaseDoesNotClaimLostHistory(t *testing.T) {
	s := testStore(t)
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM meta WHERE key LIKE 'stats.coverage.%'`); got != 0 {
		t.Fatal("鲜库不应宣称遗失旧历史", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM sqlite_temp_schema WHERE name='migration_business_now'`); got != 0 {
		t.Fatal("迁移临时数据未清理", got)
	}
}
