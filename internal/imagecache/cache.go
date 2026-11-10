// Package imagecache 保存和清理本地缩略图，不下载图片。
package imagecache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrTooLarge     = errors.New("缩略图超过缓存容量")
	ErrInvalidLimit = errors.New("缓存容量不能为负")
	ErrInvalidKey   = errors.New("缓存键不能为空")
	ErrUnsafeFile   = errors.New("缓存条目不是独立的普通文件")
)

const (
	filePrefix = "kk-"
	fileSuffix = ".thumb"
	tempPrefix = ".kk-tmp-"
)

type Cache struct {
	dir      string
	identity os.FileInfo
	gate     chan struct{}
	now      func() time.Time
}

func New(dir string) (*Cache, error) {
	if dir == "" {
		return nil, errors.New("缓存目录不能为空")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := openDirectory(dir)
	if err != nil {
		return nil, err
	}
	identity, err := root.Stat()
	if err != nil {
		root.Close()
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, err
	}
	cache := &Cache{dir: dir, identity: identity, gate: make(chan struct{}, 1), now: time.Now}
	cache.gate <- struct{}{}
	return cache, nil
}

func filename(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filePrefix + hex.EncodeToString(sum[:]) + fileSuffix
}

func (c *Cache) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.gate:
		if err := ctx.Err(); err != nil {
			c.release()
			return err
		}
		return nil
	}
}

func (c *Cache) release() { c.gate <- struct{}{} }

func openDirectory(dir string) (*os.File, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), dir), nil
}

func (c *Cache) openDirectory() (*os.File, error) {
	root, err := openDirectory(c.dir)
	if err != nil {
		return nil, err
	}
	identity, err := root.Stat()
	if err != nil {
		root.Close()
		return nil, err
	}
	// 只认创建实例时的目录，父目录被换成链接也不能把操作带到别处。
	if !os.SameFile(c.identity, identity) {
		root.Close()
		return nil, ErrUnsafeFile
	}
	return root, nil
}

func regularAt(root *os.File, name string) (unix.Stat_t, error) {
	var info unix.Stat_t
	if err := unix.Fstatat(int(root.Fd()), name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return info, err
	}
	// 硬链接也不能更新时间，否则会改到目录外同一文件的元数据。
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 || info.Size < 0 {
		return info, ErrUnsafeFile
	}
	return info, nil
}

func touch(file *os.File, at time.Time) error {
	value := unix.NsecToTimeval(at.UnixNano())
	return unix.Futimes(int(file.Fd()), []unix.Timeval{value, value})
}

func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()
	if key == "" {
		return nil, ErrInvalidKey
	}
	root, err := c.openDirectory()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filename(key)
	if _, err := regularAt(root, name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 {
		return nil, ErrUnsafeFile
	}
	data, err := io.ReadAll(contextReader{ctx, file})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := touch(file, c.now()); err != nil {
		return nil, err
	}
	return data, nil
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

func (c *Cache) Put(ctx context.Context, key string, data []byte, maxBytes int64) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	if key == "" {
		return ErrInvalidKey
	}
	if maxBytes < 0 {
		return ErrInvalidLimit
	}
	if int64(len(data)) > maxBytes {
		return ErrTooLarge
	}
	root, err := c.openDirectory()
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := scan(ctx, root); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temporary := tempPrefix + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(root.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer file.Close()
	defer unix.Unlinkat(int(root.Fd()), temporary, 0)
	for remaining := data; len(remaining) > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := remaining[:min(len(remaining), 64<<10)]
		written, err := file.Write(chunk)
		if err != nil {
			return err
		}
		if written != len(chunk) {
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	if err := touch(file, c.now()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat(int(root.Fd()), temporary, int(root.Fd()), filename(key)); err != nil {
		return err
	}
	if err := root.Sync(); err != nil {
		return err
	}
	_, err = prune(ctx, root, maxBytes)
	return err
}

func (c *Cache) Prune(ctx context.Context, maxBytes int64) (int64, error) {
	if err := c.acquire(ctx); err != nil {
		return 0, err
	}
	defer c.release()
	if maxBytes < 0 {
		return 0, ErrInvalidLimit
	}
	root, err := c.openDirectory()
	if err != nil {
		return 0, err
	}
	defer root.Close()
	return prune(ctx, root, maxBytes)
}

func (c *Cache) Clear(ctx context.Context) (int64, error) { return c.Prune(ctx, 0) }

type entry struct {
	name string
	size int64
	used time.Time
	temp bool
}

func isHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func scan(ctx context.Context, root *os.File) ([]entry, error) {
	// 独立打开读取游标，同一操作多次扫描不会沿用目录的末尾位置。
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "缓存目录")
	defer directory.Close()
	files, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]entry, 0, len(files))
	var problems error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return entries, errors.Join(problems, err)
		}
		name := file.Name()
		temporary := strings.HasPrefix(name, tempPrefix) && isHex(strings.TrimPrefix(name, tempPrefix), 32)
		managed := strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix) && isHex(strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix), 64)
		if !temporary && !managed {
			continue
		}
		info, err := regularAt(root, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			problems = errors.Join(problems, err)
			continue
		}
		entries = append(entries, entry{name, info.Size, time.Unix(info.Mtim.Sec, info.Mtim.Nsec), temporary})
	}
	return entries, problems
}

func prune(ctx context.Context, root *os.File, maxBytes int64) (deleted int64, resultErr error) {
	entries, resultErr := scan(ctx, root)
	var total int64
	for _, item := range entries {
		if item.size > math.MaxInt64-total {
			return 0, errors.Join(resultErr, ErrUnsafeFile)
		}
		total += item.size
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].temp != entries[j].temp {
			return entries[i].temp
		}
		if !entries[i].used.Equal(entries[j].used) {
			return entries[i].used.Before(entries[j].used)
		}
		return entries[i].name < entries[j].name
	})
	defer func() {
		if deleted > 0 {
			resultErr = errors.Join(resultErr, root.Sync())
		}
	}()
	for _, item := range entries {
		if !item.temp && total <= maxBytes && maxBytes != 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return deleted, errors.Join(resultErr, err)
		}
		if _, err := regularAt(root, item.name); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, err)
			}
			continue
		}
		if err := unix.Unlinkat(int(root.Fd()), item.name, 0); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, err)
			}
			continue
		}
		deleted++
		total -= item.size
	}
	return deleted, resultErr
}
