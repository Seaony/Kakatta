package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"golang.org/x/sys/unix"
)

func (s *Service) GetSystemStatus(ctx context.Context) (SystemStatus, error) {
	if err := ctx.Err(); err != nil {
		return SystemStatus{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	clock, err := readQuotaClockTx(ctx, tx)
	if err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	observed := time.Now()
	if s.now != nil {
		observed = s.now()
	}
	clock, now, err := quota.AdvanceClock(clock, observed, timezone)
	if err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	limits, err := quotaLimits(snapshot)
	if err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	var pages int64
	if err := json.Unmarshal(snapshot.Values["crawl.global_pages"], &pages); err != nil || pages < 1 {
		return SystemStatus{}, storageFailure(errors.New("页面额度配置损坏"))
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name FROM accounts ORDER BY id`)
	if err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	accounts := []AccountQuotaView{}
	for rows.Next() {
		var account AccountQuotaView
		if err := rows.Scan(&account.AccountID, &account.Name); err != nil {
			rows.Close()
			return SystemStatus{}, storageFailure(err)
		}
		accounts = append(accounts, account)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return SystemStatus{}, storageFailure(readErr)
	}
	if closeErr != nil {
		return SystemStatus{}, storageFailure(closeErr)
	}
	for i := range accounts {
		view, _, err := loadQuotaTx(ctx, tx, accounts[i].AccountID, clock, now, limits)
		if err != nil {
			return SystemStatus{}, storageFailure(err)
		}
		accounts[i].Quota = view
	}
	if err := tx.Commit(); err != nil {
		return SystemStatus{}, storageFailure(err)
	}
	// 外部资源读数不占着数据库连接，浏览器数量只取真正持有名额的运行快照。
	scheduler, browsers := s.runtimeStatusSnapshot()
	browsers.Limit = pages
	resources := readSystemResources(s.dataDir)
	if err := ctx.Err(); err != nil {
		return SystemStatus{}, err
	}
	return SystemStatus{AsOf: now.UnixMilli(), Scheduler: scheduler, Browsers: browsers,
		Configuration: statusConfiguration(s.settings.Definitions(), snapshot), Quotas: accounts, Resources: resources}, nil
}

func statusConfiguration(definitions []settings.Definition, snapshot settings.Snapshot) []ConfigurationCheck {
	warnings := make(map[string]locales.Message, len(snapshot.Warnings))
	for _, warning := range snapshot.Warnings {
		warnings[warning.Key] = warning.Reason
	}
	checks := make([]ConfigurationCheck, 0, len(definitions))
	for _, definition := range definitions {
		check := ConfigurationCheck{Key: definition.Key, OK: true}
		if reason, exists := warnings[definition.Key]; exists {
			args := make(map[string]any, len(reason.Args))
			for key, value := range reason.Args {
				args[key] = value
			}
			reason.Args, check.Reason = args, &reason
		}
		checks = append(checks, check)
	}
	return checks
}

func readSystemResources(dataDir string) SystemResources {
	resources := SystemResources{}
	resources.MemoryTotalBytes, resources.MemoryUsedBytes = platformMemory()
	var disk unix.Statfs_t
	if unix.Statfs(dataDir, &disk) == nil && disk.Bsize > 0 {
		resources.DiskTotalBytes = statusDiskBytes(disk.Blocks, uint64(disk.Bsize))
		resources.DiskFreeBytes = statusDiskBytes(disk.Bavail, uint64(disk.Bsize))
	}
	return resources
}

func statusDiskBytes(blocks, blockSize uint64) *int64 {
	if blockSize == 0 || blocks > math.MaxInt64/blockSize {
		return nil
	}
	value := int64(blocks * blockSize)
	return &value
}
