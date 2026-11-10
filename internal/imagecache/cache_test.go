package imagecache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	cache, err := New(filepath.Join(t.TempDir(), "cache", "images"))
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func mustPut(t *testing.T, cache *Cache, key, value string, limit int64) {
	t.Helper()
	if err := cache.Put(t.Context(), key, []byte(value), limit); err != nil {
		t.Fatal(err)
	}
}

func mustGet(t *testing.T, cache *Cache, key, want string) {
	t.Helper()
	got, err := cache.Get(t.Context(), key)
	if err != nil || string(got) != want {
		t.Fatalf("读取缓存：得到 %q，期望 %q，错误 %v", got, want, err)
	}
}

func mustMissing(t *testing.T, cache *Cache, key string) {
	t.Helper()
	if _, err := cache.Get(t.Context(), key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缓存应不存在，错误为 %v", err)
	}
}

func TestNewCreatesDirectoryAndRejectsUnsafeRoot(t *testing.T) {
	cache := newTestCache(t)
	info, err := os.Stat(cache.dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("缓存目录不正确：%v %v", info, err)
	}
	if _, err := New(""); err == nil {
		t.Fatal("空目录应拒绝")
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file); err == nil {
		t.Fatal("普通文件不能作缓存目录")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(cache.dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(link); err == nil {
		t.Fatal("不应跟随缓存目录符号链接")
	}
}

func TestStoreReplaceAndIndependentReads(t *testing.T) {
	cache := newTestCache(t)
	mustPut(t, cache, "商品一", "old", 100)
	got, err := cache.Get(t.Context(), "商品一")
	if err != nil {
		t.Fatal(err)
	}
	got[0] = 'x'
	mustGet(t, cache, "商品一", "old")
	mustPut(t, cache, "商品一", "replacement", 100)
	mustGet(t, cache, "商品一", "replacement")
	entries, err := os.ReadDir(cache.dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("替换后不应留下旧文件或临时文件：%v %v", entries, err)
	}
	info, err := entries[0].Info()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("缓存文件权限不正确：%v %v", info, err)
	}
}

func TestOversizeAndInvalidInputsDoNotAlterCache(t *testing.T) {
	cache := newTestCache(t)
	mustPut(t, cache, "old", "123", 3)
	if err := cache.Put(t.Context(), "old", []byte("1234"), 3); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超限图片没有拒绝：%v", err)
	}
	mustGet(t, cache, "old", "123")
	if err := cache.Put(t.Context(), "new", []byte("1234"), 3); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	mustMissing(t, cache, "new")
	if err := cache.Put(t.Context(), "old", []byte("x"), -1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatal(err)
	}
	if _, err := cache.Prune(t.Context(), -1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatal(err)
	}
	if err := cache.Put(t.Context(), "", nil, 1); !errors.Is(err, ErrInvalidKey) {
		t.Fatal(err)
	}
	if _, err := cache.Get(t.Context(), ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatal(err)
	}
	mustGet(t, cache, "old", "123")
}

func TestPruneUsesPersistentLastReadAndActualBytes(t *testing.T) {
	cache := newTestCache(t)
	base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	for i, key := range []string{"a", "b", "c"} {
		mustPut(t, cache, key, key+key+key, 9)
		at := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(filepath.Join(cache.dir, filename(key)), at, at); err != nil {
			t.Fatal(err)
		}
	}
	cache.now = func() time.Time { return base.Add(time.Hour) }
	mustGet(t, cache, "a", "aaa")
	deleted, err := cache.Prune(t.Context(), 6)
	if err != nil || deleted != 1 {
		t.Fatalf("按字节淘汰失败：%d %v", deleted, err)
	}
	mustMissing(t, cache, "b")
	// 新实例只能靠磁盘上的使用时间继续判断，不能依赖旧实例内存。
	reopened, err := New(cache.dir)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err = reopened.Prune(t.Context(), 3)
	if err != nil || deleted != 1 {
		t.Fatalf("重开后淘汰失败：%d %v", deleted, err)
	}
	mustMissing(t, reopened, "c")
	mustGet(t, reopened, "a", "aaa")
}

func TestPutPrunesAndZeroBudgetRemovesEmptyFiles(t *testing.T) {
	cache := newTestCache(t)
	cache.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	mustPut(t, cache, "a", "123", 3)
	cache.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	mustPut(t, cache, "b", "abc", 3)
	mustMissing(t, cache, "a")
	mustGet(t, cache, "b", "abc")
	mustPut(t, cache, "empty", "", 3)
	deleted, err := cache.Prune(t.Context(), 0)
	if err != nil || deleted != 2 {
		t.Fatalf("零容量应清除全部缓存条目：%d %v", deleted, err)
	}
}

func TestClearOnlyDeletesOwnedFilesAndReportsPartialFailure(t *testing.T) {
	cache := newTestCache(t)
	mustPut(t, cache, "owned", "image", 100)
	foreign := filepath.Join(cache.dir, "keep.txt")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(cache.dir, filename("directory"))
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	deleted, err := cache.Clear(t.Context())
	if deleted != 1 || !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("部分清除应返回真实数量和错误：%d %v", deleted, err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "keep" {
		t.Fatal("删除了非缓存文件", err)
	}
	if info, err := os.Stat(unsafe); err != nil || !info.IsDir() {
		t.Fatal("删除了特殊目录", err)
	}
}

func TestOrphanTemporaryFilesAreReclaimed(t *testing.T) {
	cache := newTestCache(t)
	owned := filepath.Join(cache.dir, ".kk-tmp-"+strings.Repeat("a", 32))
	foreign := filepath.Join(cache.dir, ".kk-tmp-unknown")
	for _, file := range []string{owned, foreign} {
		if err := os.WriteFile(file, []byte("unfinished"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := cache.Prune(t.Context(), 1<<20)
	if err != nil || deleted != 1 {
		t.Fatalf("应清理本包遗留临时文件：%d %v", deleted, err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("不应清理未知名称", err)
	}
}

func TestKeysCannotSelectPathsAndLinksAreNeverFollowed(t *testing.T) {
	cache := newTestCache(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../outside", "/tmp/outside", "a/b\\c\x00x", "https://example.invalid/a?token=private"} {
		mustPut(t, cache, key, "safe", 100)
		mustGet(t, cache, key, "safe")
	}
	linkName := filepath.Join(cache.dir, filename("linked"))
	if err := os.Symlink(outside, linkName); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(t.Context(), "linked"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("不应读取符号链接：%v", err)
	}
	if err := cache.Put(t.Context(), "linked", []byte("bad"), 100); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("不应替换特殊缓存条目：%v", err)
	}
	if _, err := cache.Clear(t.Context()); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("应报告跳过特殊条目：%v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatal("缓存操作改动了目录外文件", err)
	}
	if info, err := os.Lstat(linkName); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("不应删除特殊条目", err)
	}
}

func TestCancelledOperationsAndDirectoryFailureHaveNoSideEffects(t *testing.T) {
	cache := newTestCache(t)
	mustPut(t, cache, "old", "old", 100)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cache.Put(ctx, "new", []byte("new"), 100); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := cache.Get(ctx, "old"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := cache.Prune(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := cache.Clear(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	backup := cache.dir + ".backup"
	if err := os.Rename(cache.dir, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(t.Context(), "new", []byte("new"), 100); err == nil {
		t.Fatal("目录异常应返回错误")
	}
	if err := os.Remove(cache.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, cache.dir); err != nil {
		t.Fatal(err)
	}
	mustGet(t, cache, "old", "old")
	mustMissing(t, cache, "new")
}

func TestWaitingOperationsCanBeCancelled(t *testing.T) {
	cache := newTestCache(t)
	mustPut(t, cache, "old", "old", 100)
	if err := cache.acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer cache.release()
	operations := []func(context.Context) error{
		func(ctx context.Context) error { _, err := cache.Get(ctx, "old"); return err },
		func(ctx context.Context) error { return cache.Put(ctx, "new", []byte("new"), 100) },
		func(ctx context.Context) error { _, err := cache.Prune(ctx, 0); return err },
		func(ctx context.Context) error { _, err := cache.Clear(ctx); return err },
	}
	results := make(chan error, len(operations))
	for _, operation := range operations {
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			results <- operation(ctx)
		}()
	}
	for range operations {
		select {
		case err := <-results:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("等待中的缓存操作没有及时响应取消")
		}
	}
}

func TestHardLinkAndReplacedRootDoNotModifyOutsideFiles(t *testing.T) {
	cache := newTestCache(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(cache.dir, filename("hardlink"))); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(t.Context(), "hardlink"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatal("硬链接不应被读作缓存", err)
	}
	if deleted, err := cache.Clear(t.Context()); deleted != 0 || !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("硬链接不应删除：%d %v", deleted, err)
	}
	after, err := os.Stat(outside)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("更新了目录外硬链接的时间", err)
	}
	backup := cache.dir + ".backup"
	if err := os.Rename(cache.dir, backup); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	outsideCache := filepath.Join(other, filename("a"))
	if err := os.WriteFile(outsideCache, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, cache.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(t.Context(), "a"); err == nil {
		t.Fatal("不应跟随被替换的缓存根目录")
	}
	if deleted, err := cache.Clear(t.Context()); deleted != 0 || err == nil {
		t.Fatalf("不应清理目录外缓存：%d %v", deleted, err)
	}
	if err := cache.Put(t.Context(), "a", []byte("bad"), 100); err == nil {
		t.Fatal("不应写入目录外缓存")
	}
	if value, err := os.ReadFile(outsideCache); err != nil || string(value) != "keep" {
		t.Fatal("改动了目录外文件", err)
	}
}

func TestReplacedAncestorCannotRedirectCache(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	cache, err := New(filepath.Join(parent, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+".moved"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "images", filename("key"))
	if err := os.WriteFile(outsideFile, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(t.Context(), "key"); err == nil {
		t.Fatal("父目录被换成链接后，不应跟到另一份目录")
	}
	if err := cache.Put(t.Context(), "key", []byte("bad"), 100); err == nil {
		t.Fatal("不应写入被替换父目录指向的位置")
	}
	if deleted, err := cache.Clear(t.Context()); deleted != 0 || err == nil {
		t.Fatalf("不应删除被替换父目录里的文件：%d %v", deleted, err)
	}
	if value, err := os.ReadFile(outsideFile); err != nil || string(value) != "keep" {
		t.Fatal("改动了目录外文件", err)
	}
}

func TestConcurrentOperationsKeepCompleteFilesAndRespectBudget(t *testing.T) {
	cache := newTestCache(t)
	var group sync.WaitGroup
	errorsSeen := make(chan error, 32)
	for worker := 0; worker < 24; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for round := 0; round < 8; round++ {
				key := fmt.Sprintf("item-%d", (worker+round)%8)
				value := bytes.Repeat([]byte{byte('a' + worker%20)}, 64)
				if err := cache.Put(t.Context(), key, value, 256); err != nil {
					errorsSeen <- err
					return
				}
				got, err := cache.Get(t.Context(), key)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					errorsSeen <- err
					return
				}
				if err == nil && (len(got) != 64 || !bytes.Equal(got, bytes.Repeat(got[:1], 64))) {
					errorsSeen <- errors.New("读取到了部分写入的缓存")
					return
				}
				err = nil
				if worker%3 == 0 {
					_, err = cache.Prune(t.Context(), 128)
				} else if worker%7 == 0 {
					_, err = cache.Clear(t.Context())
				}
				if err != nil {
					errorsSeen <- err
					return
				}
			}
		}(worker)
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if _, err := cache.Prune(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache.dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("留下了临时文件 %s", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	if total > 128 {
		t.Fatalf("容量没有限制住：%d", total)
	}
}
