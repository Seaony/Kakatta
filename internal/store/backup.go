package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Backup 生成包含 WAL 已提交内容的一致快照，不覆盖已有文件。
func (s *Store) Backup(ctx context.Context, path string) (err error) {
	path, err = filePath(path)
	if err != nil {
		return err
	}
	if path == s.path {
		return errors.New("备份路径不能是正在使用的数据库")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("备份文件必须尚不存在：%w", err)
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(path)
		}
	}()
	return s.vacuumInto(ctx, path)
}

func (s *Store) vacuumInto(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("生成数据库快照：%w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

var migrationBackupName = regexp.MustCompile(`^kakatta-upgrade-v[0-9]{6,}-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9]+\.db$`)

func (s *Store) backupBeforeMigration(ctx context.Context, version int) (err error) {
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	prefix := fmt.Sprintf("kakatta-upgrade-v%06d-%s-", version, time.Now().UTC().Format("20060102T150405.000000000Z"))
	f, err := os.CreateTemp(dir, prefix+"*.pending")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Close(); err != nil {
		return err
	}
	if err = s.vacuumInto(ctx, name); err != nil {
		return err
	}
	final := strings.TrimSuffix(name, ".pending") + ".db"
	if _, err := os.Lstat(final); !errors.Is(err, os.ErrNotExist) {
		return errors.New("数据库升级快照的目标已经存在")
	}
	if err = os.Rename(name, final); err != nil {
		return err
	}
	if err = syncDirectory(dir); err != nil {
		return err
	}
	return pruneBackups(dir, final)
}

func pruneBackups(dir, latest string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type backup struct {
		path string
		at   time.Time
	}
	var files []backup
	for _, entry := range entries {
		if !migrationBackupName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, backup{filepath.Join(dir, entry.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if (files[i].path == latest) != (files[j].path == latest) {
			return files[i].path == latest
		}
		if files[i].at.Equal(files[j].at) {
			return files[i].path > files[j].path
		}
		return files[i].at.After(files[j].at)
	})
	for _, file := range files[min(3, len(files)):] {
		if err := os.Remove(file.path); err != nil {
			return err
		}
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
