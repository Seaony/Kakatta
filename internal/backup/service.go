package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidDependencies = errors.New("backup.invalid_dependencies")
	ErrClosed              = errors.New("backup.closed")
)

// WorkDirName 是数据目录下专用于备份临时文件的保留名称。
const WorkDirName = ".backup-work"

const (
	workDirMarker     = ".kakatta-backup-v1"
	exportWorkPrefix  = ".backup-export-"
	restoreWorkPrefix = ".backup-restore-"
)

type Service struct {
	deps       Dependencies
	workDir    string
	ctx        context.Context
	cancel     context.CancelFunc
	slot       chan struct{}
	mu         sync.Mutex
	closed     bool
	cleanupErr error
	pending    *restoreCandidate
	jobs       sync.WaitGroup
	closeOnce  sync.Once
}

func New(deps Dependencies) (*Service, error) {
	if deps.DataDir == "" || deps.AppVersion == "" || deps.SchemaVersion < 1 || deps.Runtime == nil {
		return nil, ErrInvalidDependencies
	}
	value := reflect.ValueOf(deps.Runtime)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return nil, ErrInvalidDependencies
	}
	path, err := backupDataDir(deps.DataDir)
	if err != nil {
		return nil, err
	}
	workDir := filepath.Join(path, WorkDirName)
	if err := os.Mkdir(workDir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fileFailure(err)
	}
	if err := validatePrivateDir(workDir); err != nil {
		return nil, err
	}
	owned, _, err := inspectWorkDir(workDir)
	if err != nil {
		return nil, err
	}
	if !owned {
		marker := filepath.Join(workDir, workDirMarker)
		if err := os.Mkdir(marker, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fileFailure(err)
		}
		if err := validateWorkMarker(marker); err != nil {
			return nil, err
		}
	}
	// 先把所有权标记写稳，再允许写候选或快照。
	dir, err := os.Open(workDir)
	if err != nil {
		return nil, fileFailure(err)
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return nil, fileFailure(err)
	}
	deps.DataDir = path
	if deps.Now == nil {
		deps.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{deps: deps, workDir: workDir, ctx: ctx, cancel: cancel, slot: make(chan struct{}, 1)}, nil
}

func backupDataDir(dataDir string) (string, error) {
	if dataDir == "" {
		return "", ErrInvalidDependencies
	}
	path, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fileFailure(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fileFailure(err)
	}
	if !info.IsDir() {
		return "", ErrInvalidDependencies
	}
	return path, nil
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fileFailure(err)
	}
	if info.Mode() != os.ModeDir|0700 {
		return ErrInvalidDependencies
	}
	return nil
}

func validateWorkMarker(path string) error {
	if err := validatePrivateDir(path); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fileFailure(err)
	}
	if len(entries) != 0 {
		return ErrInvalidDependencies
	}
	return nil
}

func inspectWorkDir(path string) (owned bool, jobs []string, err error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, nil, fileFailure(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == workDirMarker {
			if err := validateWorkMarker(filepath.Join(path, name)); err != nil {
				return false, nil, err
			}
			owned = true
			continue
		}
		if !isWorkJobName(name) {
			return false, nil, ErrInvalidDependencies
		}
		if err := validatePrivateDir(filepath.Join(path, name)); err != nil {
			return false, nil, err
		}
		jobs = append(jobs, name)
	}
	if !owned && len(jobs) != 0 {
		return false, nil, ErrInvalidDependencies
	}
	return owned, jobs, nil
}

func isWorkJobName(name string) bool {
	suffix, found := strings.CutPrefix(name, exportWorkPrefix)
	if !found {
		suffix, found = strings.CutPrefix(name, restoreWorkPrefix)
	}
	if !found {
		return false
	}
	// MkdirTemp 在前缀后加 uint32 的十进制表示。
	number, err := strconv.ParseUint(suffix, 10, 32)
	return err == nil && strconv.FormatUint(number, 10) == suffix
}

// CleanupInterrupted 在启动持有服务和恢复排他锁、处理持久恢复记录后，清理中断留下的备份工作目录。
func CleanupInterrupted(dataDir string) error {
	path, err := backupDataDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	workDir := filepath.Join(path, WorkDirName)
	if err := validatePrivateDir(workDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	owned, jobs, err := inspectWorkDir(workDir)
	if err != nil {
		return err
	}
	for _, name := range jobs {
		if err := os.RemoveAll(filepath.Join(workDir, name)); err != nil {
			return fileFailure(err)
		}
	}
	// 最后删除标记，清理失败后还能认领并重试剩余的工作目录。
	if owned {
		if err := os.Remove(filepath.Join(workDir, workDirMarker)); err != nil {
			return fileFailure(err)
		}
	}
	if err := os.Remove(workDir); err != nil {
		return fileFailure(err)
	}
	return nil
}

func (s *Service) begin(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	s.jobs.Add(1)
	s.mu.Unlock()
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	release := func() { stop(); cancel(); s.jobs.Done() }
	select {
	case s.slot <- struct{}{}:
	case <-work.Done():
		release()
		return nil, nil, work.Err()
	}
	if err := work.Err(); err != nil {
		<-s.slot
		release()
		return nil, nil, err
	}
	return work, func() { <-s.slot; release() }, nil
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.jobs.Wait()
		s.mu.Lock()
		pending := s.pending
		s.pending = nil
		if pending != nil {
			pending.timer.Stop()
		}
		s.mu.Unlock()
		_ = s.cleanRestore(pending)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupErr
}

func (s *Service) String() string   { return "备份服务" }
func (s *Service) GoString() string { return s.String() }
