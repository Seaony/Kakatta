package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func versionTwoNotificationDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path, db := versionOneDB(t)
	execute(t, db, businessSchema)
	execute(t, db, `INSERT INTO schema_migrations(version,applied_at) VALUES(2,2)`)
	execute(t, db, `INSERT INTO system_events(id,kind,subject_id,message,created_at,resolved_at,notified_at) VALUES
		(1,'account_verify',7,'{"code":"verify","args":{}}',10,NULL,NULL),
		(2,'account_login',8,'{"code":"login","args":{}}',20,40,30)`)
	execute(t, db, `INSERT INTO deliveries(id,at,channel_id,kind,item_count,ok,error) VALUES
		(1,11,99,'system',1,0,'{"code":"failure","args":{}}'),
		(2,30,99,'system',1,1,NULL)`)
	return path, db
}

func TestSystemNotificationUpgradePreservesOldDataWithoutInferringRetries(t *testing.T) {
	path, old := versionTwoNotificationDB(t)
	old.Close()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if got := scalar[int](t, s.DB(), `SELECT max(version) FROM schema_migrations`); got != len(migrations) {
		t.Fatal("没有升级到当前版本", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM system_events WHERE failure_count=0 AND first_failed_at IS NULL`); got != 2 {
		t.Fatal("迁移从旧发送记录推断了失败轮次", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM deliveries WHERE event_id IS NULL`); got != 2 {
		t.Fatal("迁移猜测了旧投递对应的系统事件", got)
	}
	if got := scalar[string](t, s.DB(), `SELECT message FROM system_events WHERE id=1`); got != `{"code":"verify","args":{}}` {
		t.Fatal("旧事件消息被改写", got)
	}
	if scalar[int](t, s.DB(), `SELECT resolved_at FROM system_events WHERE id=2`) != 40 ||
		scalar[int](t, s.DB(), `SELECT notified_at FROM system_events WHERE id=2`) != 30 ||
		scalar[string](t, s.DB(), `SELECT error FROM deliveries WHERE id=1`) != `{"code":"failure","args":{}}` {
		t.Fatal("旧事件和投递结论没有完整保留")
	}
	backups := ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))
	if len(backups) != 1 {
		t.Fatal("升级前没有保存一份旧库快照", backups)
	}
	before, err := sql.Open("sqlite", dataSource(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, before, `SELECT max(version) FROM schema_migrations`) != 2 ||
		scalar[int](t, before, `SELECT count(*) FROM pragma_table_info('deliveries') WHERE name='event_id'`) != 0 ||
		scalar[int](t, before, `SELECT count(*) FROM system_events`) != 2 {
		t.Fatal("备份不是升级前结构和内容")
	}
	before.Close()
	if _, err := s.DB().Exec(`UPDATE system_events SET failure_count=-1 WHERE id=1`); err == nil {
		t.Fatal("系统事件失败次数不能为负")
	}
	execute(t, s.DB(), `UPDATE system_events SET failure_count=2,first_failed_at=50 WHERE id=1`)
	execute(t, s.DB(), `UPDATE deliveries SET event_id=1 WHERE id=1`)
	s = reopenStore(t, s)
	if scalar[int](t, s.DB(), `SELECT failure_count FROM system_events WHERE id=1`) != 2 ||
		scalar[int](t, s.DB(), `SELECT first_failed_at FROM system_events WHERE id=1`) != 50 ||
		scalar[int](t, s.DB(), `SELECT event_id FROM deliveries WHERE id=1`) != 1 {
		t.Fatal("重开丢失系统通知失败记录或关联")
	}
	if len(ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))) != 1 {
		t.Fatal("重开重复执行了升级")
	}
	execute(t, s.DB(), `DELETE FROM system_events WHERE id=1`)
	if scalar[int](t, s.DB(), `SELECT event_id FROM deliveries WHERE id=1`) != 1 {
		t.Fatal("删除系统事件丢失历史投递关联")
	}
	execute(t, s.DB(), `UPDATE deliveries SET event_id=999 WHERE id=1`)
}

func TestSystemNotificationUpgradeRollsBackColumnsAndIndex(t *testing.T) {
	path, old := versionTwoNotificationDB(t)
	old.Close()
	steps := append(append([]migration{}, migrations...), migration{version: len(migrations) + 1, sql: `INVALID SYSTEM NOTIFICATION MIGRATION`})
	if s, err := open(t.Context(), path, steps); err == nil {
		s.Close()
		t.Fatal("迁移失败不能提交系统事件的新列")
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if scalar[int](t, db, `SELECT max(version) FROM schema_migrations`) != 2 ||
		scalar[int](t, db, `SELECT count(*) FROM pragma_table_info('system_events') WHERE name IN ('failure_count','first_failed_at')`) != 0 ||
		scalar[int](t, db, `SELECT count(*) FROM pragma_table_info('deliveries') WHERE name='event_id'`) != 0 ||
		scalar[int](t, db, `SELECT count(*) FROM sqlite_schema WHERE name='deliveries_event_id_at'`) != 0 {
		t.Fatal("失败后留下新版本、字段或索引")
	}
	if scalar[int](t, db, `SELECT notified_at FROM system_events WHERE id=2`) != 30 ||
		scalar[int](t, db, `SELECT count(*) FROM deliveries`) != 2 {
		t.Fatal("失败后旧记录没有完整保留")
	}
}
