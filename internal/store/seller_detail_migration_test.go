package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func versionFiveSellerDetailDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path, db := versionFourDeliveryDB(t)
	execute(t, db, deliveryFormatSchema)
	execute(t, db, `INSERT INTO schema_migrations(version,applied_at) VALUES(5,5)`)
	return path, db
}

func TestSellerDetailColumnStartsUnknownAndSurvivesBackup(t *testing.T) {
	s := testStore(t)
	if SchemaVersion() != 6 {
		t.Fatal("详情卖家指标没有独立的第六版迁移")
	}
	execute(t, s.DB(), `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at) VALUES('item','原商品','链接',1,2)`)
	if scalar[int](t, s.DB(), `SELECT seller_detail_json IS NULL FROM listings WHERE item_id='item'`) != 1 {
		t.Fatal("旧商品指标被编成了零值")
	}
	const raw = `{"item_count":0,"has_sold_num_integer":3,"avg_reply_30d_long":0,"new_good_ratio_rate":"100%","last_visit_time":"刚刚来过","idle_fish_credit_level":"L3","zhima_auth":false}`
	execute(t, s.DB(), `UPDATE listings SET seller_detail_json=? WHERE item_id='item'`, raw)
	path := filepath.Join(t.TempDir(), "seller-detail-backup.db")
	if err := s.Backup(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(t.Context(), path, SchemaVersion()); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if scalar[string](t, copy.DB(), `SELECT seller_detail_json FROM listings WHERE item_id='item'`) != raw {
		t.Fatal("备份恢复改变了原始指标")
	}
}

func TestSellerDetailUpgradePreservesUnknownAndVersionFiveSnapshot(t *testing.T) {
	path, old := versionFiveSellerDetailDB(t)
	old.Close()
	if err := ValidateBackup(t.Context(), path, 5); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if scalar[int](t, s.DB(), `SELECT seller_detail_json IS NULL FROM listings WHERE item_id='camera'`) != 1 ||
		scalar[string](t, s.DB(), `SELECT seller_id FROM listings WHERE item_id='camera'`) != "seller" ||
		scalar[int](t, s.DB(), `SELECT score FROM findings WHERE id=1`) != 88 ||
		scalar[int](t, s.DB(), `SELECT count(*) FROM deliveries`) != 3 {
		t.Fatal("升级回填指标或改变了旧商品与投递历史")
	}
	backups := ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))
	if len(backups) != 1 {
		t.Fatal("没有保留唯一第五版升级快照", backups)
	}
	before, err := sql.Open("sqlite", dataSource(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	if scalar[int](t, before, `SELECT max(version) FROM schema_migrations`) != 5 ||
		scalar[int](t, before, `SELECT count(*) FROM pragma_table_info('listings') WHERE name='seller_detail_json'`) != 0 ||
		scalar[int](t, before, `SELECT count(*) FROM listings`) != 1 {
		t.Fatal("升级前快照不再是第五版原库")
	}
	s = reopenStore(t, s)
	if scalar[int](t, s.DB(), `SELECT seller_detail_json IS NULL FROM listings WHERE item_id='camera'`) != 1 || len(ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))) != 1 {
		t.Fatal("重开回填未知指标或重复做升级")
	}
}

func TestSellerDetailFailedUpgradeRollsBackToVersionFive(t *testing.T) {
	path, old := versionFiveSellerDetailDB(t)
	old.Close()
	steps := append(append([]migration{}, migrations...), migration{version: len(migrations) + 1, sql: `INVALID SELLER DETAIL MIGRATION`})
	if s, err := open(t.Context(), path, steps); err == nil {
		s.Close()
		t.Fatal("失败迁移被提交")
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if scalar[int](t, db, `SELECT max(version) FROM schema_migrations`) != 5 ||
		scalar[int](t, db, `SELECT count(*) FROM pragma_table_info('listings') WHERE name='seller_detail_json'`) != 0 ||
		scalar[int](t, db, `SELECT score FROM findings WHERE id=1`) != 88 {
		t.Fatal("失败迁移没有完整保留旧结构和数据")
	}
}
