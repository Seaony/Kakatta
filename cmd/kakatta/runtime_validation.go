package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func validateRuntimeCandidate(ctx context.Context, path string, key [backup.MasterKeyBytes]byte, schemaVersion int, env settings.Environment) (err error) {
	defer clear(key[:])
	// 原库通过可信结构与完整性检查后，才允许在上传的私有候选里补迁移。
	if err := store.ValidateBackup(ctx, path, schemaVersion); err != nil {
		return err
	}
	database, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var busy, pages, checkpointed int
		checkpointErr := database.DB().QueryRowContext(cleanup, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &pages, &checkpointed)
		if checkpointErr == nil && (busy != 0 || pages != checkpointed) {
			checkpointErr = errors.New("候选数据库检查点未完成")
		}
		err = errors.Join(err, checkpointErr, database.Close())
		if canceled := ctx.Err(); canceled != nil && !errors.Is(err, canceled) {
			err = errors.Join(err, canceled)
		}
	}()
	cipher, err := security.NewCipher(key[:])
	if err != nil {
		return err
	}
	db := database.DB()
	if _, err := db.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
		return err
	}
	// 业务校验使用迁移后的完整结构，不能靠启动服务补写或修复候选。
	if err := service.ValidateStored(ctx, db, cipher, env); err != nil {
		return err
	}
	if err := modelservice.ValidateStored(ctx, db, cipher); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := notification.ValidateCandidate(ctx, tx, cipher); err != nil {
		return err
	}
	hasAdmin, err := auth.ValidateStoredTx(ctx, tx, cipher)
	if err != nil {
		return err
	}
	if !hasAdmin {
		return errors.New("备份候选没有管理员")
	}
	return tx.Commit()
}
