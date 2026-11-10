package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// SchemaVersion 返回本程序可信迁移的最新版本。
func SchemaVersion() int { return len(migrations) }

// ValidateBackup 只读检查备份结构，不迁移，也不校验 JSON 和密文的业务内容。
func ValidateBackup(ctx context.Context, path string, expectedSchema int) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if canceled := ctx.Err(); canceled != nil && !errors.Is(err, canceled) {
			err = errors.Join(err, canceled)
		}
	}()
	if expectedSchema < 1 || expectedSchema > SchemaVersion() {
		return fmt.Errorf("不支持备份数据库版本 %d", expectedSchema)
	}
	path, err = filePath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("备份数据库必须是普通文件")
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	u.RawQuery = url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "trusted_schema(0)", "foreign_keys(1)", "busy_timeout(5000)"}}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { err = errors.Join(err, db.Close()) }()
	want, err := trustedBackupSchema(ctx, expectedSchema)
	if err != nil {
		return err
	}
	if err = checkBackupSchema(ctx, db, expectedSchema, want); err != nil {
		return err
	}
	// VACUUM 会重建索引，必须先拒绝原库的损坏，不能修好后再宣称它有效。
	if err = checkBackupIntegrity(ctx, db); err != nil {
		return err
	}
	if err = checkForeignKeys(ctx, db); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kakatta-backup-check-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	copyPath := filepath.Join(dir, "check.db")
	if err = copyBackupForValidation(ctx, &Store{db: db, path: path}, copyPath); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	u.Path = filepath.ToSlash(copyPath)
	q := u.Query()
	q.Set("mode", "rw")
	u.RawQuery = q.Encode()
	copyDB, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	copyDB.SetMaxOpenConns(1)
	copyDB.SetMaxIdleConns(1)
	defer func() { err = errors.Join(err, copyDB.Close()) }()
	if err = checkBackupSchema(ctx, copyDB, expectedSchema, want); err != nil {
		return err
	}
	// SQLite 只读打开时不保留 CHECK 表达式，另查可写打开但禁止 SQL 写入的私有副本。
	if err = checkBackupIntegrity(ctx, copyDB); err != nil {
		return err
	}
	if err = checkForeignKeys(ctx, copyDB); err != nil {
		return err
	}
	return ctx.Err()
}

func copyBackupForValidation(ctx context.Context, source *Store, path string) (err error) {
	// 只为 VACUUM INTO 写独立副本暂关软限制；源连接的 mode=ro 始终不变。
	if _, err = source.db.ExecContext(ctx, "PRAGMA query_only=OFF"); err != nil {
		return err
	}
	defer func() {
		_, resetErr := source.db.ExecContext(context.WithoutCancel(ctx), "PRAGMA query_only=ON")
		err = errors.Join(err, resetErr)
	}()
	return source.Backup(ctx, path)
}

type backupSchemaObject struct {
	kind, name, table string
	ddl               sql.NullString
}

func checkBackupSchema(ctx context.Context, db queryer, expectedSchema int, want map[string]backupSchemaObject) error {
	got, err := readBackupSchema(ctx, db)
	if err != nil {
		return err
	}
	// 先核对完整结构，再查询版本和数据，避免执行上传库中的视图或触发器。
	for key, object := range want {
		candidate, ok := got[key]
		if !ok {
			return fmt.Errorf("备份数据库缺少结构：%s", object.name)
		}
		if candidate != object {
			return fmt.Errorf("备份数据库结构不符：%s", object.name)
		}
	}
	var extraIndexes []backupSchemaObject
	for key, object := range got {
		if _, ok := want[key]; ok {
			continue
		}
		if object.kind != "index" || !object.ddl.Valid {
			return fmt.Errorf("备份数据库有未知结构：%s", object.name)
		}
		if _, ok := want["table\x00"+object.table]; !ok {
			return fmt.Errorf("备份索引引用未知表：%s", object.name)
		}
		extraIndexes = append(extraIndexes, object)
	}
	for _, object := range extraIndexes {
		if err := ordinaryBackupIndex(ctx, db, object); err != nil {
			return err
		}
	}
	version, err := readVersion(ctx, db, migrations, false)
	if err != nil {
		return err
	}
	if version != expectedSchema {
		return fmt.Errorf("备份清单版本 %d 与数据库版本 %d 不一致", expectedSchema, version)
	}
	return nil
}

func readBackupSchema(ctx context.Context, db queryer) (map[string]backupSchemaObject, error) {
	rows, err := db.QueryContext(ctx, "SELECT type,name,tbl_name,sql FROM sqlite_schema")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := map[string]backupSchemaObject{}
	for rows.Next() {
		var object backupSchemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.table, &object.ddl); err != nil {
			return nil, err
		}
		key := object.kind + "\x00" + object.name
		if _, exists := objects[key]; exists {
			return nil, fmt.Errorf("备份数据库结构重复：%s", object.name)
		}
		objects[key] = object
	}
	return objects, rows.Err()
}

func trustedBackupSchema(ctx context.Context, version int) (map[string]backupSchemaObject, error) {
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	// 只有本仓库的迁移会在这个独立内存库执行，上传的 DDL 始终只用于比较。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, migrationTable); err != nil {
		return nil, err
	}
	for _, step := range migrations[:version] {
		if _, err := db.ExecContext(ctx, step.sql); err != nil {
			return nil, err
		}
	}
	return readBackupSchema(ctx, db)
}

func ordinaryBackupIndex(ctx context.Context, db queryer, object backupSchemaObject) error {
	reject := fmt.Errorf("备份包含不支持的额外索引：%s", object.name)
	var unique, partial int
	var origin string
	if err := db.QueryRowContext(ctx, `SELECT "unique",origin,partial FROM pragma_index_list(?) WHERE name=?`, object.table, object.name).Scan(&unique, &origin, &partial); err != nil {
		return err
	}
	if unique != 0 || partial != 0 || origin != "c" {
		return reject
	}
	columns, err := columnNames(ctx, db, object.table)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT cid,name,coll,key FROM pragma_index_xinfo(?)`, object.name)
	if err != nil {
		return err
	}
	defer rows.Close()
	keys := 0
	for rows.Next() {
		var cid, key int
		var name, collation sql.NullString
		if err := rows.Scan(&cid, &name, &collation, &key); err != nil {
			return err
		}
		if key == 0 {
			continue
		}
		if cid < 0 || !name.Valid || !columns[name.String] || !collation.Valid || collation.String != "BINARY" {
			return reject
		}
		keys++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if keys == 0 {
		return reject
	}
	return nil
}

func checkBackupIntegrity(ctx context.Context, db queryer) error {
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return errors.New("备份数据库完整性检查失败：" + result)
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New("备份数据库完整性检查没有结果")
	}
	return nil
}
