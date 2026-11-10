package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/config"
)

type restoreEntry struct {
	Mode os.FileMode
	Body string
}
type restoreFixture struct {
	dataDir string
	plan    backup.RestorePlan
	before  map[string]restoreEntry
	inodes  map[string]os.FileInfo
	lock    os.FileInfo
}

func writeRestoreFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func restoreTree(t *testing.T, path string) map[string]restoreEntry {
	t.Helper()
	result := map[string]restoreEntry{}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return result
	}
	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, current)
		body := ""
		if info.Mode()&os.ModeSymlink != 0 {
			body, err = os.Readlink(current)
		} else if info.Mode().IsRegular() {
			var raw []byte
			raw, err = os.ReadFile(current)
			body = string(raw)
		}
		if err != nil {
			return err
		}
		result[rel] = restoreEntry{info.Mode(), body}
		return nil
	})
	if err != nil {
		t.Fatal("无法读取测试文件树")
	}
	return result
}

func fixedRestoreTrees(t *testing.T, dir string) map[string]restoreEntry {
	t.Helper()
	result := map[string]restoreEntry{}
	for _, r := range restoreResources {
		for name, entry := range restoreTree(t, filepath.Join(dir, r.live)) {
			result[r.name+"/"+name] = entry
		}
	}
	return result
}

func restoreTestPlan(dataDir string, defaultKey bool) backup.RestorePlan {
	source := backup.KeyExternalFile
	if defaultKey {
		source = backup.KeyDefaultFile
	}
	return backup.RestorePlan{DatabasePath: filepath.Join(dataDir, "candidate", "verified.db"), MasterKey: sha256.Sum256([]byte("local-new-restore-key")), KeySource: source, ExpectedGeneration: "old-generation"}
}

func newRestoreFixture(t *testing.T, defaultKey, sidecars bool) restoreFixture {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"browser", "cache/images", "candidate"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{"kakatta.db": "old-database", "secret.key": "old-default-key", "external.key": "external-key-unchanged", "browser/nested/profile": "old-browser", "cache/images/image": "old-image", "candidate/verified.db": "new-database"} {
		writeRestoreFile(t, filepath.Join(dir, path), body)
	}
	if sidecars {
		writeRestoreFile(t, filepath.Join(dir, "kakatta.db-wal"), "old-wal")
		writeRestoreFile(t, filepath.Join(dir, "kakatta.db-shm"), "old-shm")
	}
	if err := os.Symlink(filepath.Join(dir, "external.key"), filepath.Join(dir, "browser", "profile-link")); err != nil {
		t.Fatal(err)
	}
	lock, err := config.AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	info, err := os.Stat(filepath.Join(dir, ".server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	f := restoreFixture{dataDir: dir, plan: restoreTestPlan(dir, defaultKey), before: fixedRestoreTrees(t, dir), inodes: map[string]os.FileInfo{}, lock: info}
	for _, r := range restoreResources {
		if info, err := os.Lstat(filepath.Join(dir, r.live)); err == nil {
			f.inodes[r.name] = info
		}
	}
	return f
}

func (fixture restoreFixture) assertOld(t *testing.T) {
	t.Helper()
	if !reflect.DeepEqual(fixture.before, fixedRestoreTrees(t, fixture.dataDir)) {
		t.Fatal("恢复后没有保持整套原物")
	}
	for _, r := range restoreResources {
		if previous := fixture.inodes[r.name]; previous != nil {
			info, err := os.Lstat(filepath.Join(fixture.dataDir, r.live))
			if err != nil || !os.SameFile(previous, info) {
				t.Fatal("旧资源inode改变", r.name)
			}
		}
	}
	fixture.assertUntouched(t)
}

func (fixture restoreFixture) assertUntouched(t *testing.T) {
	t.Helper()
	lock, err := os.Stat(filepath.Join(fixture.dataDir, ".server.lock"))
	if err != nil || !os.SameFile(fixture.lock, lock) {
		t.Fatal("移动或替换了服务锁")
	}
	if raw, err := os.ReadFile(filepath.Join(fixture.dataDir, "external.key")); err != nil || string(raw) != "external-key-unchanged" {
		t.Fatal("外部密钥被改动")
	}
}

func (fixture restoreFixture) assertNew(t *testing.T) {
	t.Helper()
	if raw, err := os.ReadFile(filepath.Join(fixture.dataDir, "kakatta.db")); err != nil || string(raw) != "new-database" {
		t.Fatal("新数据库不完整")
	}
	for _, name := range []string{"browser", "cache/images"} {
		entries, err := os.ReadDir(filepath.Join(fixture.dataDir, name))
		if err != nil || len(entries) != 0 {
			t.Fatal("新浏览器或图片目录不为空")
		}
	}
	want := []byte("old-default-key")
	if fixture.plan.KeySource == backup.KeyDefaultFile {
		want = fixture.plan.MasterKey[:]
	}
	if raw, err := os.ReadFile(filepath.Join(fixture.dataDir, "secret.key")); err != nil || !bytes.Equal(raw, want) {
		t.Fatal("密钥文件代数错误")
	}
	fixture.assertUntouched(t)
}

func repeatRestoreRecovery(t *testing.T, dir string) {
	t.Helper()
	for range 2 {
		pending, err := recoverRestoreFiles(t.Context(), dir)
		if err != nil || pending {
			t.Fatal("恢复未能幂等完成", err)
		}
	}
}

func TestRestoreFilesRollbackAndCommit(t *testing.T) {
	for _, defaultKey := range []bool{false, true} {
		for _, sidecars := range []bool{false, true} {
			for _, committed := range []bool{false, true} {
				t.Run(strconv.FormatBool(defaultKey)+"_"+strconv.FormatBool(sidecars)+"_"+strconv.FormatBool(committed), func(t *testing.T) {
					fixture := newRestoreFixture(t, defaultKey, sidecars)
					sourceInfo, _ := os.Stat(fixture.plan.DatabasePath)
					f, err := stageRestoreFiles(t.Context(), fixture.dataDir, fixture.plan)
					if err != nil {
						t.Fatal(err)
					}
					fixture.assertOld(t)
					if _, err := os.Lstat(fixture.plan.DatabasePath); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("暂存未消费候选文件")
					}
					if err := f.install(t.Context()); err != nil {
						t.Fatal(err)
					}
					info, _ := os.Stat(filepath.Join(fixture.dataDir, "kakatta.db"))
					if !os.SameFile(sourceInfo, info) {
						t.Fatal("换入没有使用同文件系统rename")
					}
					writeRestoreFile(t, filepath.Join(fixture.dataDir, "kakatta.db-wal"), "new-wal")
					writeRestoreFile(t, filepath.Join(fixture.dataDir, "kakatta.db-shm"), "new-shm")
					if committed {
						if may, err := f.commit(); err != nil || !may {
							t.Fatal(err)
						}
					} else {
						if err := f.rollback(t.Context()); err != nil {
							t.Fatal(err)
						}
					}
					repeatRestoreRecovery(t, fixture.dataDir)
					if committed {
						fixture.assertNew(t)
					} else {
						fixture.assertOld(t)
					}
				})
			}
		}
	}
}

func TestRestoreFilesEveryForwardBoundary(t *testing.T) {
	baseline := newRestoreFixture(t, true, true)
	var events []string
	f, err := stageRestoreFilesWithHook(t.Context(), baseline.dataDir, baseline.plan, func(label string) error { events = append(events, label); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := f.install(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commit(); err != nil {
		t.Fatal(err)
	}
	if err := f.cleanupCommitted(t.Context()); err != nil {
		t.Fatal(err)
	}
	for target, label := range events {
		t.Run(strconv.Itoa(target)+"_"+strings.ReplaceAll(label, "/", "_"), func(t *testing.T) {
			fixture := newRestoreFixture(t, true, true)
			index := 0
			failure := errors.New("local-fixed-io-failure")
			hook := func(string) error {
				current := index
				index++
				if current == target {
					return failure
				}
				return nil
			}
			f, err := stageRestoreFilesWithHook(t.Context(), fixture.dataDir, fixture.plan, hook)
			if err == nil {
				err = f.install(t.Context())
			}
			if err == nil {
				_, err = f.commit()
			}
			if err == nil {
				err = f.cleanupCommitted(t.Context())
			}
			if !errors.Is(err, failure) || strings.Contains(err.Error(), fixture.dataDir) {
				t.Fatal("故障未返回安全原原因")
			}
			newData := f != nil && f.mayHaveCommitted
			if newData {
				if err := f.rollback(t.Context()); !errors.Is(err, errRestoreFilesState) {
					t.Fatal("提交后仍允许普通回滚")
				}
			}
			repeatRestoreRecovery(t, fixture.dataDir)
			if newData {
				fixture.assertNew(t)
			} else {
				fixture.assertOld(t)
			}
		})
	}
}

func TestRestoreFilesEveryRollbackBoundary(t *testing.T) {
	prepare := func(t *testing.T) (restoreFixture, *restoreFiles) {
		fixture := newRestoreFixture(t, true, true)
		f, err := stageRestoreFiles(t.Context(), fixture.dataDir, fixture.plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.install(t.Context()); err != nil {
			t.Fatal(err)
		}
		writeRestoreFile(t, filepath.Join(fixture.dataDir, "kakatta.db-wal"), "new-wal")
		writeRestoreFile(t, filepath.Join(fixture.dataDir, "kakatta.db-shm"), "new-shm")
		return fixture, f
	}
	_, baseline := prepare(t)
	var events []string
	baseline.after = func(label string) error { events = append(events, label); return nil }
	if err := baseline.rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	for target, label := range events {
		t.Run(strconv.Itoa(target)+"_"+strings.ReplaceAll(label, "/", "_"), func(t *testing.T) {
			fixture, f := prepare(t)
			index := 0
			failure := errors.New("local-fixed-rollback-failure")
			f.after = func(string) error {
				current := index
				index++
				if current == target {
					return failure
				}
				return nil
			}
			if err := f.rollback(t.Context()); !errors.Is(err, failure) {
				t.Fatal("回滚故障未被注入")
			}
			repeatRestoreRecovery(t, fixture.dataDir)
			fixture.assertOld(t)
		})
	}
}

func TestRestoreFilesCancellationCrossDeviceAndMissingTransaction(t *testing.T) {
	for _, kind := range []string{"before_pending", "after_pending", "cross_device", "missing_pending_dir", "committed_dir_cleaned"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newRestoreFixture(t, true, true)
			f, err := stageRestoreFiles(t.Context(), fixture.dataDir, fixture.plan)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch kind {
			case "before_pending":
				cancel()
			case "after_pending":
				f.after = func(label string) error {
					if label == "pending.journal.parent_sync.close" {
						cancel()
					}
					return nil
				}
			case "cross_device":
				f.after = func(label string) error {
					if label == "old.db.before_rename" {
						return syscall.EXDEV
					}
					return nil
				}
			}
			err = f.install(ctx)
			if kind == "before_pending" || kind == "after_pending" || kind == "cross_device" {
				if err == nil {
					t.Fatal("取消或跨文件系统错误被忽略")
				}
				if kind == "cross_device" {
					if !errors.Is(err, syscall.EXDEV) {
						t.Fatal("跨文件系统错误原因丢失")
					}
					fixture.assertOld(t)
				}
				f.after = nil
				if err := f.rollback(t.Context()); err != nil {
					t.Fatal(err)
				}
				repeatRestoreRecovery(t, fixture.dataDir)
				fixture.assertOld(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing_pending_dir" {
				if err := os.RemoveAll(f.dir); err != nil {
					t.Fatal(err)
				}
				if _, err := recoverRestoreFiles(t.Context(), fixture.dataDir); err == nil {
					t.Fatal("pending丢失事务目录却被放行")
				}
			} else {
				if _, err := f.commit(); err != nil {
					t.Fatal(err)
				}
				if err := f.cleanStage(t.Context()); err != nil {
					t.Fatal(err)
				}
				repeatRestoreRecovery(t, fixture.dataDir)
				fixture.assertNew(t)
			}
		})
	}
}

func TestRestoreFilesStrictJournalAndSymlinks(t *testing.T) {
	for _, kind := range []string{"duplicate", "missing", "null", "unknown", "future", "invalid_id", "db_missing", "key_conflict", "data_link", "root_link", "transaction_link", "new_link", "old_link"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newRestoreFixture(t, true, true)
			f, err := stageRestoreFiles(t.Context(), fixture.dataDir, fixture.plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.install(t.Context()); err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(fixture.dataDir, "restore-state.json")
			raw, _ := os.ReadFile(journalPath)
			switch kind {
			case "duplicate":
				raw = append([]byte(`{"version":1,`), raw[1:]...)
			case "missing":
				raw = bytes.Replace(raw, []byte(`,"wal":true`), nil, 1)
			case "null":
				raw = bytes.Replace(raw, []byte(`"wal":true`), []byte(`"wal":null`), 1)
			case "unknown":
				raw = append([]byte(`{"other":false,`), raw[1:]...)
			case "future":
				raw = bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1)
			case "invalid_id":
				var j map[string]any
				_ = json.Unmarshal(raw, &j)
				j["id"] = "../outside"
				raw, _ = json.Marshal(j)
			case "db_missing":
				raw = bytes.Replace(raw, []byte(`"db":true`), []byte(`"db":false`), 1)
			case "key_conflict":
				raw = bytes.Replace(raw, []byte(`"key":true`), []byte(`"key":false`), 1)
			default:
				path := map[string]string{"data_link": fixture.dataDir, "root_link": filepath.Join(fixture.dataDir, ".restore"), "transaction_link": f.dir, "new_link": filepath.Join(f.dir, "new"), "old_link": filepath.Join(f.dir, "old")}[kind]
				target := filepath.Join(t.TempDir(), "preserved")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if !strings.HasSuffix(kind, "_link") {
				if err := os.WriteFile(journalPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := restoreTree(t, fixture.dataDir)
			if _, err := recoverRestoreFiles(t.Context(), fixture.dataDir); err == nil {
				t.Fatal("坏journal或顶层链接未被拒绝")
			}
			if !reflect.DeepEqual(before, restoreTree(t, fixture.dataDir)) {
				t.Fatal("拒绝无效状态时修改了文件")
			}
		})
	}
}

func TestRestoreFilesCrashHelper(t *testing.T) {
	dir, target := os.Getenv("RESTORE_FILES_TEST_DIR"), os.Getenv("RESTORE_FILES_TEST_CRASH")
	if dir == "" || target == "" {
		return
	}
	hook := func(label string) error {
		if label == target {
			os.Exit(91)
		}
		return nil
	}
	f, err := stageRestoreFilesWithHook(t.Context(), dir, restoreTestPlan(dir, true), hook)
	if err == nil {
		err = f.install(t.Context())
	}
	if err == nil && strings.HasPrefix(target, "rollback.") {
		writeRestoreFile(t, filepath.Join(dir, "kakatta.db-wal"), "new-wal")
		writeRestoreFile(t, filepath.Join(dir, "kakatta.db-shm"), "new-shm")
		err = f.rollback(t.Context())
	} else if err == nil {
		_, err = f.commit()
		if err == nil {
			err = f.cleanupCommitted(t.Context())
		}
	}
	t.Fatal("没有命中预定退出边界", err)
}

func TestRestoreFilesSubprocessCrashes(t *testing.T) {
	for _, target := range []string{"pending.journal.rename", "old.db.rename", "new.db.rename", "rollback.wal.rename", "committed.journal.rename", "cleanup.old.browser.remove", "cleanup.transaction.remove"} {
		t.Run(target, func(t *testing.T) {
			fixture := newRestoreFixture(t, true, true)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRestoreFilesCrashHelper$")
			cmd.Env = append(os.Environ(), "RESTORE_FILES_TEST_DIR="+fixture.dataDir, "RESTORE_FILES_TEST_CRASH="+target)
			if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 91 {
				t.Fatal("子进程没有在预定边界退出")
			}
			repeatRestoreRecovery(t, fixture.dataDir)
			if strings.HasPrefix(target, "committed.") || strings.HasPrefix(target, "cleanup.") {
				fixture.assertNew(t)
			} else {
				fixture.assertOld(t)
			}
		})
	}
}

func TestRestoreFilesUncertainCommitAndCommittedMissingResource(t *testing.T) {
	for _, kind := range []string{"uncertain", "missing_live", "cleanup_failure", "orphan_old", "external_default_link"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newRestoreFixture(t, kind != "external_default_link", true)
			if kind == "external_default_link" {
				if err := os.Remove(filepath.Join(fixture.dataDir, "secret.key")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(fixture.dataDir, "external.key"), filepath.Join(fixture.dataDir, "secret.key")); err != nil {
					t.Fatal(err)
				}
			}
			f, err := stageRestoreFiles(t.Context(), fixture.dataDir, fixture.plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.install(t.Context()); err != nil {
				t.Fatal(err)
			}
			if kind == "orphan_old" {
				if err := os.Remove(filepath.Join(fixture.dataDir, "restore-state.json")); err != nil {
					t.Fatal(err)
				}
				before := restoreTree(t, fixture.dataDir)
				if _, err := recoverRestoreFiles(t.Context(), fixture.dataDir); err == nil {
					t.Fatal("没有journal的非空old被删除")
				}
				if !reflect.DeepEqual(before, restoreTree(t, fixture.dataDir)) {
					t.Fatal("未知旧资料被改动")
				}
				return
			}
			if kind == "uncertain" {
				f.after = func(label string) error {
					if label == "committed.journal.parent_sync.sync" {
						return syscall.EIO
					}
					return nil
				}
			}
			may, err := f.commit()
			if kind == "uncertain" {
				if !may || !errors.Is(err, syscall.EIO) {
					t.Fatal("提交不确定性被丢失")
				}
				if err := f.rollback(t.Context()); !errors.Is(err, errRestoreFilesState) {
					t.Fatal("不确定提交允许回滚")
				}
				if err := f.cleanupCommitted(t.Context()); !errors.Is(err, errRestoreFilesState) {
					t.Fatal("不确定提交提前删除旧资料")
				}
				repeatRestoreRecovery(t, fixture.dataDir)
				fixture.assertNew(t)
				return
			}
			if err != nil || !may {
				t.Fatal(err)
			}
			if kind == "missing_live" {
				if err := os.Remove(filepath.Join(fixture.dataDir, "kakatta.db")); err != nil {
					t.Fatal(err)
				}
				before := restoreTree(t, fixture.dataDir)
				if _, err := recoverRestoreFiles(t.Context(), fixture.dataDir); err == nil {
					t.Fatal("提交后的新资源缺失仍清掉旧资料")
				}
				if !reflect.DeepEqual(before, restoreTree(t, fixture.dataDir)) {
					t.Fatal("新资源缺失时改动了恢复证据")
				}
				return
			}
			if kind == "cleanup_failure" {
				if os.Geteuid() == 0 {
					t.Skip("root不受目录权限限制")
				}
				old := filepath.Join(f.dir, "old")
				if err := os.Chmod(old, 0300); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(old, 0700)
				pending, err := recoverRestoreFiles(t.Context(), fixture.dataDir)
				if err != nil || !pending {
					t.Fatal("已提交清理失败被误报为恢复失败")
				}
				if err := os.Chmod(old, 0700); err != nil {
					t.Fatal(err)
				}
			}
			repeatRestoreRecovery(t, fixture.dataDir)
			if kind == "external_default_link" {
				if target, err := os.Readlink(filepath.Join(fixture.dataDir, "secret.key")); err != nil || target != filepath.Join(fixture.dataDir, "external.key") {
					t.Fatal("外部来源时改动了默认key路径")
				}
				fixture.assertUntouched(t)
			} else {
				fixture.assertNew(t)
			}
		})
	}
}

func TestRestoreFilesRejectsCandidateAliases(t *testing.T) {
	for _, name := range []string{"kakatta.db", "secret.key", ".server.lock"} {
		t.Run(name, func(t *testing.T) {
			fixture := newRestoreFixture(t, true, true)
			alias := filepath.Join(fixture.dataDir, "alias")
			if err := os.Symlink(fixture.dataDir, alias); err != nil {
				t.Fatal(err)
			}
			fixture.plan.DatabasePath = filepath.Join(alias, name)
			if _, err := stageRestoreFiles(t.Context(), fixture.dataDir, fixture.plan); !errors.Is(err, errRestoreFilesState) {
				t.Fatal("候选别名能在journal前移动保护资源")
			}
			fixture.assertOld(t)
		})
	}
}
