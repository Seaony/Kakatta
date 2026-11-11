package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"
)

//go:embed migrations/001_initial.sql
var initialSchema string

//go:embed migrations/002_business_runtime.sql
var businessSchema string

//go:embed migrations/003_system_notification_retry.sql
var systemNotificationSchema string

//go:embed migrations/004_finding_seller_checks.sql
var sellerChecksSchema string

//go:embed migrations/005_delivery_format_level.sql
var deliveryFormatSchema string

type migration struct {
	version int
	sql     string
}

var migrations = []migration{{version: 1, sql: initialSchema}, {version: 2, sql: businessSchema}, {version: 3, sql: systemNotificationSchema}, {version: 4, sql: sellerChecksSchema}, {version: 5, sql: deliveryFormatSchema}}

const migrationTable = `CREATE TABLE schema_migrations (
 version INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
 applied_at INTEGER NOT NULL
)`

func validateMigrations(steps []migration) error {
	if len(steps) == 0 {
		return errors.New("没有数据库迁移")
	}
	for i, step := range steps {
		if step.version != i+1 || strings.TrimSpace(step.sql) == "" {
			return errors.New("数据库迁移编号不连续或内容为空")
		}
	}
	return nil
}

func readVersion(ctx context.Context, db queryer, steps []migration, created bool) (int, error) {
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'").Scan(&exists); err != nil {
		return 0, fmt.Errorf("读取数据库版本：%w", err)
	}
	if exists == 0 {
		var objects int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
			return 0, err
		}
		if created && objects == 0 {
			return 0, nil
		}
		return 0, errors.New("数据库没有可识别的迁移版本，拒绝作为新库打开")
	}
	rows, err := db.QueryContext(ctx, "SELECT version, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, fmt.Errorf("数据库迁移记录损坏：%w", err)
	}
	defer rows.Close()
	version := 0
	for rows.Next() {
		var next int
		var appliedAt int64
		if err := rows.Scan(&next, &appliedAt); err != nil {
			return 0, fmt.Errorf("数据库迁移记录损坏：%w", err)
		}
		if next > len(steps) {
			return 0, fmt.Errorf("数据库版本 %d 比程序支持的版本 %d 新", next, len(steps))
		}
		if next != version+1 {
			return 0, errors.New("数据库迁移编号缺失或不连续")
		}
		version = next
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if version == 0 {
		return 0, errors.New("数据库迁移记录为空")
	}
	return version, nil
}

func migrate(ctx context.Context, db *sql.DB, steps []migration, from int, created bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	version, err := readVersion(ctx, tx, steps, created)
	if err != nil {
		return err
	}
	if version != from {
		return errors.New("迁移期间数据库版本发生变化，请重新打开")
	}
	if from == 0 {
		if _, err := tx.ExecContext(ctx, migrationTable); err != nil {
			return err
		}
	}
	for _, step := range steps[from:] {
		if _, err := tx.ExecContext(ctx, step.sql); err != nil {
			return fmt.Errorf("执行数据库迁移 %d：%w", step.version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", step.version, time.Now().UTC().UnixMilli()); err != nil {
			return err
		}
	}
	if err := checkColumns(ctx, tx); err != nil {
		return err
	}
	if err := checkForeignKeys(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func checkForeignKeys(ctx context.Context, db queryer) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("数据库存在外键关系损坏")
	}
	return rows.Err()
}
