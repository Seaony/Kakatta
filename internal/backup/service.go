package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

var (
	ErrInvalidDependencies = errors.New("backup.invalid_dependencies")
	ErrClosed              = errors.New("backup.closed")
)

type Service struct {
	deps       Dependencies
	ctx        context.Context
	cancel     context.CancelFunc
	slot       chan struct{}
	mu         sync.Mutex
	closed     bool
	cleanupErr error
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
	path, err := filepath.Abs(deps.DataDir)
	if err != nil {
		return nil, fileFailure(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fileFailure(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidDependencies
	}
	deps.DataDir = path
	if deps.Now == nil {
		deps.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{deps: deps, ctx: ctx, cancel: cancel, slot: make(chan struct{}, 1)}, nil
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
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupErr
}

func (s *Service) String() string   { return "备份服务" }
func (s *Service) GoString() string { return s.String() }
