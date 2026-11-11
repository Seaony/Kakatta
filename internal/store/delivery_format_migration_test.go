package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func versionFourDeliveryDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path, db := versionThreeSellerDB(t)
	execute(t, db, sellerChecksSchema)
	execute(t, db, `INSERT INTO schema_migrations(version,applied_at) VALUES(4,4)`)
	execute(t, db, `INSERT INTO deliveries(id,at,channel_id,task_id,kind,item_count,ok) VALUES(3,50,99,1,'recommended',1,1)`)
	execute(t, db, `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(3,1)`)
	return path, db
}

func TestDeliveryFormatFreshConstraintsAndBackup(t *testing.T) {
	s := testStore(t)
	for _, kind := range []string{"recommended", "system", "test"} {
		execute(t, s.DB(), `INSERT INTO deliveries(kind,item_count,ok) VALUES(?,1,1)`, kind)
	}
	if got := scalar[int](t, s.DB(), `SELECT count(*) FROM deliveries WHERE format_level=0`); got != 3 {
		t.Fatal("普通、系统与测试投递没有默认保持原格式", got)
	}
	for level := range 6 {
		execute(t, s.DB(), `UPDATE deliveries SET format_level=? WHERE id=1`, level)
		if scalar[int](t, s.DB(), `SELECT format_level FROM deliveries WHERE id=1`) != level {
			t.Fatal("合法格式级别没有保存", level)
		}
	}
	for _, level := range []any{-1, 6, 1.5, nil, "不是级别"} {
		if _, err := s.DB().ExecContext(t.Context(), `UPDATE deliveries SET format_level=? WHERE id=1`, level); err == nil {
			t.Fatal("非法格式级别写入成功", level)
		}
	}
	backup := filepath.Join(t.TempDir(), "delivery-backup.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(t.Context(), backup, SchemaVersion()); err != nil {
		t.Fatal("含格式级别的备份不通过恢复校验", err)
	}
	restored, err := Open(t.Context(), backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if scalar[int](t, restored.DB(), `SELECT format_level FROM deliveries WHERE id=1`) != 5 ||
		scalar[int](t, restored.DB(), `SELECT count(*) FROM deliveries WHERE format_level=0`) != 2 {
		t.Fatal("备份重开丢失格式级别或改写旧格式")
	}
}

func TestDeliveryFormatUpgradePreservesOldHistoryAndSnapshot(t *testing.T) {
	path, old := versionFourDeliveryDB(t)
	old.Close()
	if err := ValidateBackup(t.Context(), path, 4); err != nil {
		t.Fatal("第四版备份不能进入正常升级恢复", err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if scalar[int](t, s.DB(), `SELECT count(*) FROM deliveries WHERE format_level=0`) != 3 ||
		scalar[int](t, s.DB(), `SELECT count(*) FROM delivery_items WHERE delivery_id=3 AND finding_id=1`) != 1 ||
		scalar[string](t, s.DB(), `SELECT error FROM deliveries WHERE id=1`) != `{"code":"failure","args":{}}` ||
		scalar[int](t, s.DB(), `SELECT notified_at FROM system_events WHERE id=2`) != 30 {
		t.Fatal("升级改写或丢失原投递、关联和事件事实")
	}
	backups := ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))
	if len(backups) != 1 {
		t.Fatal("没有保留唯一升级前快照", backups)
	}
	before, err := sql.Open("sqlite", dataSource(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	if scalar[int](t, before, `SELECT max(version) FROM schema_migrations`) != 4 ||
		scalar[int](t, before, `SELECT count(*) FROM pragma_table_info('deliveries') WHERE name='format_level'`) != 0 ||
		scalar[int](t, before, `SELECT count(*) FROM deliveries`) != 3 {
		t.Fatal("升级快照不是完整第四版原库")
	}
	execute(t, s.DB(), `UPDATE deliveries SET format_level=4 WHERE id=3`)
	s = reopenStore(t, s)
	if scalar[int](t, s.DB(), `SELECT format_level FROM deliveries WHERE id=3`) != 4 ||
		len(ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))) != 1 {
		t.Fatal("重开重置格式级别或重复迁移")
	}
}

func TestDeliveryFormatUpgradeFailureKeepsVersionFour(t *testing.T) {
	path, old := versionFourDeliveryDB(t)
	old.Close()
	steps := append(append([]migration{}, migrations...), migration{version: len(migrations) + 1, sql: `INVALID DELIVERY FORMAT MIGRATION`})
	if s, err := open(t.Context(), path, steps); err == nil {
		s.Close()
		t.Fatal("失败迁移仍然提交")
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if scalar[int](t, db, `SELECT max(version) FROM schema_migrations`) != 4 ||
		scalar[int](t, db, `SELECT count(*) FROM pragma_table_info('deliveries') WHERE name='format_level'`) != 0 ||
		scalar[int](t, db, `SELECT count(*) FROM deliveries`) != 3 ||
		scalar[int](t, db, `SELECT count(*) FROM delivery_items`) != 1 {
		t.Fatal("失败迁移没有完整回滚新增列与历史")
	}
}
