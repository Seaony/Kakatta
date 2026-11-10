package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func versionThreeSellerDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path, db := versionTwoNotificationDB(t)
	execute(t, db, systemNotificationSchema)
	execute(t, db, `INSERT INTO schema_migrations(version,applied_at) VALUES(3,3)`)
	execute(t, db, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index)
		VALUES(1,'任务','相机',1,30,'ai','good',0)`)
	execute(t, db, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,seller_id) VALUES('camera','相机','商品链接',1,2,'seller')`)
	execute(t, db, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read,
		analysis_json,score,grade,seller_type) VALUES(1,1,'camera',1,1,0,'scored','done',1,'{"summary":"旧事实"}',88,'good','personal')`)
	execute(t, db, `INSERT INTO seller_profiles(seller_id,seller_credit,recent_rates_json,fetched_at) VALUES('seller','极好','[]',1)`)
	return path, db
}

func TestSellerChecksUpgradePreservesFactsAndDoesNotInferOldResult(t *testing.T) {
	path, old := versionThreeSellerDB(t)
	old.Close()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if got := scalar[int](t, s.DB(), `SELECT max(version) FROM schema_migrations`); got != len(migrations) {
		t.Fatal("没有升级到当前版本", got)
	}
	if got := scalar[int](t, s.DB(), `SELECT seller_checks_json IS NULL FROM findings WHERE id=1`); got != 1 {
		t.Fatal("旧卖家判断被按当前缓存猜测回填")
	}
	if scalar[int](t, s.DB(), `SELECT score FROM findings WHERE id=1`) != 88 ||
		scalar[string](t, s.DB(), `SELECT analysis_json FROM findings WHERE id=1`) != `{"summary":"旧事实"}` ||
		scalar[string](t, s.DB(), `SELECT seller_type FROM findings WHERE id=1`) != "personal" {
		t.Fatal("旧分数、事实或画像被迁移改写")
	}
	backups := ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))
	if len(backups) != 1 {
		t.Fatal("升级前没有一份原库快照", backups)
	}
	before, err := sql.Open("sqlite", dataSource(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, before, `SELECT max(version) FROM schema_migrations`) != 3 ||
		scalar[int](t, before, `SELECT count(*) FROM pragma_table_info('findings') WHERE name='seller_checks_json'`) != 0 ||
		scalar[int](t, before, `SELECT score FROM findings WHERE id=1`) != 88 {
		t.Fatal("升级快照不是第三版原结构和数据")
	}
	before.Close()
	const saved = `{"type":"personal","checks":[],"bonus_eligible":true,"reasons":[]}`
	execute(t, s.DB(), `UPDATE findings SET seller_checks_json=? WHERE id=1`, saved)
	execute(t, s.DB(), `DELETE FROM seller_profiles`)
	s = reopenStore(t, s)
	if scalar[string](t, s.DB(), `SELECT seller_checks_json FROM findings WHERE id=1`) != saved ||
		scalar[int](t, s.DB(), `SELECT score FROM findings WHERE id=1`) != 88 {
		t.Fatal("卖家缓存清除或重开丢失分析时判断")
	}
	if len(ownedBackups(t, filepath.Join(filepath.Dir(path), "backups"))) != 1 {
		t.Fatal("当前版本重开重复执行迁移")
	}
	execute(t, s.DB(), `UPDATE findings SET seller_checks_json=NULL WHERE id=1`)
}

func TestSellerChecksUpgradeRollsBackAddedColumnAndPreservesVersionThree(t *testing.T) {
	path, old := versionThreeSellerDB(t)
	old.Close()
	steps := append(append([]migration{}, migrations...), migration{version: len(migrations) + 1, sql: `INVALID SELLER CHECK MIGRATION`})
	if s, err := open(t.Context(), path, steps); err == nil {
		s.Close()
		t.Fatal("故障迁移不能提交部分新结构")
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if scalar[int](t, db, `SELECT max(version) FROM schema_migrations`) != 3 ||
		scalar[int](t, db, `SELECT count(*) FROM pragma_table_info('findings') WHERE name='seller_checks_json'`) != 0 ||
		scalar[int](t, db, `SELECT score FROM findings WHERE id=1`) != 88 ||
		scalar[string](t, db, `SELECT analysis_json FROM findings WHERE id=1`) != `{"summary":"旧事实"}` {
		t.Fatal("失败后旧版本、结构或数据没有完整保留")
	}
	if scalar[int](t, db, `SELECT failure_count FROM system_events WHERE id=1`) != 0 {
		t.Fatal("失败迁移破坏了原有第三版字段")
	}
}
