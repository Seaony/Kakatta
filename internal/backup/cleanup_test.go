package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupNewKeepsExistingWorkDirectoryContents(t *testing.T) {
	s, _, _ := exportFixture(t)
	work := filepath.Join(s.deps.DataDir, ".backup-work")
	info, err := os.Lstat(work)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("构造服务后缺少私有备份工作目录", err)
	}
	marker := filepath.Join(work, ".kakatta-backup-v1")
	markerInfo, err := os.Lstat(marker)
	if err != nil || markerInfo.Mode() != os.ModeDir|0700 {
		t.Fatal("构造服务后缺少私有所有权标记", err)
	}
	if entries, err := os.ReadDir(marker); err != nil || len(entries) != 0 {
		t.Fatal("所有权标记应是空目录", err)
	}
	active := filepath.Join(work, ".backup-export-1")
	if err := os.Mkdir(active, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(active, "raw.db")
	if err := os.WriteFile(snapshot, []byte("正在使用的快照"), 0600); err != nil {
		t.Fatal(err)
	}
	other, err := New(s.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(snapshot); err != nil || string(data) != "正在使用的快照" {
		t.Fatal("构造或关闭另一个服务删除了正在使用的快照", err)
	}
}

func TestCleanupInterruptedRemovesOnlyReservedWorkDirectory(t *testing.T) {
	dataDir := t.TempDir()
	workDir := filepath.Join(dataDir, ".backup-work")
	if err := os.MkdirAll(filepath.Join(workDir, ".kakatta-backup-v1"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(workDir, ".backup-restore-4294967295", "candidate", DatabaseEntry),
		filepath.Join(workDir, ".backup-export-0", "raw.db"),
		filepath.Join(workDir, ".backup-export-0", "clean.db"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("中断遗留的数据库"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(workDir, ".backup-restore-4294967295", "outside")); err != nil {
		t.Fatal(err)
	}
	protected := []string{
		filepath.Join(external, "keep"),
		filepath.Join(dataDir, ".backup-restore-old", DatabaseEntry),
		filepath.Join(dataDir, ".backup-export-old", "raw.db"),
		filepath.Join(dataDir, ".backup-work-other", "keep"),
		filepath.Join(dataDir, DatabaseEntry),
	}
	for _, path := range protected {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("保留"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupInterrupted(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("中断的备份工作目录没有清理", err)
	}
	for _, path := range protected {
		if data, err := os.ReadFile(path); err != nil || string(data) != "保留" {
			t.Fatal("清理修改了专用目录外的文件", err)
		}
	}
	if err := CleanupInterrupted(dataDir); err != nil {
		t.Fatal("工作目录不存在时应成功", err)
	}
}

func TestCleanupInterruptedAndNewRejectUnsafeWorkDirectory(t *testing.T) {
	for _, kind := range []string{"普通文件", "目录链接", "非私有目录"} {
		t.Run(kind, func(t *testing.T) {
			dataDir := t.TempDir()
			workDir := filepath.Join(dataDir, ".backup-work")
			marker := workDir
			switch kind {
			case "目录链接":
				outside := t.TempDir()
				marker = filepath.Join(outside, "keep")
				if err := os.Symlink(outside, workDir); err != nil {
					t.Fatal(err)
				}
			case "非私有目录":
				marker = filepath.Join(workDir, "keep")
				if err := os.Mkdir(workDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(workDir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(marker, []byte("保留"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(workDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := CleanupInterrupted(dataDir); !errors.Is(err, ErrInvalidDependencies) {
				t.Fatal("清理没有拒绝无效工作目录", err)
			}
			s, err := New(Dependencies{DataDir: dataDir, AppVersion: "test", SchemaVersion: 1, Runtime: &exportRuntime{}})
			if s != nil || !errors.Is(err, ErrInvalidDependencies) {
				t.Fatal("构造没有拒绝无效工作目录", err)
			}
			after, err := os.Lstat(workDir)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("无效工作目录被移动、删除或改了权限", err)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "保留" {
				t.Fatal("无效路径内的原有内容被修改", err)
			}
		})
	}
}

func TestCleanupInterruptedRejectsDataDirectoryLinkAndReportsIOErrors(t *testing.T) {
	dataDir := t.TempDir()
	workDir := filepath.Join(dataDir, ".backup-work")
	if err := os.Mkdir(workDir, 0700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	link := filepath.Join(root, "data-link")
	if err := os.Symlink(dataDir, link); err != nil {
		t.Fatal(err)
	}
	if err := CleanupInterrupted(link); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatal("清理没有拒绝数据目录链接", err)
	}
	if _, err := os.Lstat(workDir); err != nil {
		t.Fatal("链接指向的工作目录被清理", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupInterrupted(filepath.Join(file, "data")); !errors.Is(err, ErrIO) || strings.Contains(err.Error(), root) {
		t.Fatal("访问失败没有返回安全的I/O错误", err)
	}
	if err := CleanupInterrupted(""); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatal("空路径被当作数据目录", err)
	}
	if err := CleanupInterrupted(filepath.Join(root, "missing")); err != nil {
		t.Fatal("数据目录不存在时应成功", err)
	}
}

func TestCleanupInterruptedReportsFailedRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("需要非特权用户才能复现文件删除被拒绝")
	}
	dataDir := t.TempDir()
	workDir := filepath.Join(dataDir, ".backup-work")
	marker := filepath.Join(workDir, ".kakatta-backup-v1")
	if err := os.MkdirAll(marker, 0700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(workDir, ".backup-export-1", "private")
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(blocked, "raw.db")
	if err := os.WriteFile(file, []byte("遗留快照"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0700)
	if err := CleanupInterrupted(dataDir); !errors.Is(err, ErrIO) || !errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), dataDir) {
		t.Fatal("删除被拒绝时未返回安全的I/O错误", err)
	}
	if _, err := os.Lstat(file); err != nil {
		t.Fatal("测试未留下无法删除的快照", err)
	}
	if _, err := os.Lstat(marker); err != nil {
		t.Fatal("部分清理失败后丢失了所有权标记", err)
	}
	if err := os.Chmod(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CleanupInterrupted(dataDir); err != nil {
		t.Fatal("权限恢复后仍无法清理", err)
	}
}

func TestCleanupInterruptedRejectsUnownedAndUnknownWorkContents(t *testing.T) {
	for _, tc := range []struct {
		name, child, kind string
		owned             bool
	}{
		{"同名用户目录", "notes.db", "file", false},
		{"未标记的同名前缀", ".backup-export-1", "dir", false},
		{"标记目录有陌生文件", "notes.db", "file", true},
		{"非数字后缀", ".backup-export-recent", "dir", true},
		{"前导零", ".backup-export-01", "dir", true},
		{"超出实际后缀范围", ".backup-restore-4294967296", "dir", true},
		{"工作名称被文件占用", ".backup-export-1", "file", true},
		{"工作名称被链接占用", ".backup-export-1", "link", true},
		{"工作子目录权限不私有", ".backup-export-1", "open-dir", true},
		{"所有权标记非空", ".kakatta-backup-v1", "dir", false},
		{"所有权标记为链接", ".kakatta-backup-v1", "link", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			workDir := filepath.Join(dataDir, ".backup-work")
			if err := os.Mkdir(workDir, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.owned {
				if err := os.Mkdir(filepath.Join(workDir, ".kakatta-backup-v1"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			child := filepath.Join(workDir, tc.child)
			keep := child
			switch tc.kind {
			case "dir", "open-dir":
				if err := os.Mkdir(child, 0700); err != nil {
					t.Fatal(err)
				}
				if tc.kind == "open-dir" {
					if err := os.Chmod(child, 0755); err != nil {
						t.Fatal(err)
					}
				}
				keep = filepath.Join(child, "keep")
			case "link":
				outside := t.TempDir()
				if err := os.Symlink(outside, child); err != nil {
					t.Fatal(err)
				}
				keep = filepath.Join(outside, "keep")
			}
			if err := os.WriteFile(keep, []byte("保留"), 0600); err != nil {
				t.Fatal(err)
			}
			validJob := filepath.Join(workDir, ".backup-restore-7")
			if err := os.Mkdir(validJob, 0700); err != nil {
				t.Fatal(err)
			}
			if err := CleanupInterrupted(dataDir); !errors.Is(err, ErrInvalidDependencies) {
				t.Error("清理没有拒绝未认领或含未知子项的目录", err)
			}
			s, err := New(Dependencies{DataDir: dataDir, AppVersion: "test", SchemaVersion: 1, Runtime: &exportRuntime{}})
			if s != nil || !errors.Is(err, ErrInvalidDependencies) {
				t.Error("构造认领了未授权的工作目录", err)
			}
			for _, path := range []string{child, validJob} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("工作目录尚未完全验证就删除了子项", err)
				}
			}
			if data, err := os.ReadFile(keep); err != nil || string(data) != "保留" {
				t.Fatal("未知子项的内容被修改", err)
			}
		})
	}
}

func TestCleanupInterruptedRemovesEmptyUnmarkedWorkDirectory(t *testing.T) {
	dataDir := t.TempDir()
	workDir := filepath.Join(dataDir, ".backup-work")
	if err := os.Mkdir(workDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CleanupInterrupted(dataDir); err != nil {
		t.Fatal("初始化中断留下的空目录应可清理", err)
	}
	if _, err := os.Lstat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("初始化中断留下的空目录没有清理", err)
	}
	s, err := New(Dependencies{DataDir: dataDir, AppVersion: "test", SchemaVersion: 1, Runtime: &exportRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Lstat(filepath.Join(workDir, ".kakatta-backup-v1")); err != nil {
		t.Fatal("重新初始化未写入所有权标记", err)
	}
}
