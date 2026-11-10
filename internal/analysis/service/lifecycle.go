package modelservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

type Service struct {
	db       *sql.DB
	settings *appsettings.Store
	cipher   *security.Cipher
	now      func() time.Time
	publish  func(string, *locales.Message, json.RawMessage) error
	system   func(context.Context, int64) error

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	jobs   int
	idle   chan struct{}
}

func New(ctx context.Context, deps Dependencies) (_ *Service, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deps.DB == nil || deps.Settings == nil || deps.Cipher == nil || deps.Publish == nil || deps.System == nil {
		return nil, storageFailure(errors.New("模型服务依赖不完整"))
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	life, cancel := context.WithCancel(ctx)
	s := &Service{db: deps.DB, settings: deps.Settings, cipher: deps.Cipher, now: deps.Now,
		publish: deps.Publish, system: deps.System, ctx: life, cancel: cancel, idle: make(chan struct{})}
	close(s.idle)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, _, err := s.observeNowTx(ctx, tx)
	if err == nil {
		err = initializeProviderTokens(ctx, tx)
	}
	if err == nil {
		err = appsettings.SaveClock(ctx, tx, clock)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, storageFailure(err)
	}
	return s, nil
}

func (s *Service) beginJob(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, nil, closedService()
	}
	if s.jobs == 0 {
		s.idle = make(chan struct{})
	}
	s.jobs++
	s.mu.Unlock()
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	var once sync.Once
	done := func() {
		once.Do(func() {
			stop()
			cancel()
			s.mu.Lock()
			s.jobs--
			if s.jobs == 0 {
				close(s.idle)
			}
			s.mu.Unlock()
		})
	}
	return work, done, nil
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) observeNowTx(ctx context.Context, tx *sql.Tx) (settings.Snapshot, quota.ClockState, time.Time, error) {
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return settings.Snapshot{}, quota.ClockState{}, time.Time{}, err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return settings.Snapshot{}, quota.ClockState{}, time.Time{}, err
	}
	clock, now, err := appsettings.ObserveClock(ctx, tx, s.now(), timezone, false)
	return snapshot, clock, now, err
}

func (s *Service) publishProviderChange(id int64) error {
	data := map[string]any{"provider_id": id}
	if id == 0 {
		data = map[string]any{"all": true}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	logCallbackFailure(s.publish("ai.providers.changed", nil, raw))
	return nil
}

func (s *Service) publishSystemChange(ctx context.Context, eventID int64, raised bool) error {
	raw, err := json.Marshal(map[string]any{"event_id": eventID})
	if err != nil {
		return err
	}
	result := s.publish("system_event.changed", nil, raw)
	if raised {
		// 事务已经提交；调用方取消不能丢掉已保存事件的发送回执。
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		result = errors.Join(result, s.system(cleanup, eventID))
	}
	logCallbackFailure(result)
	return nil
}

func logCallbackFailure(err error) {
	if err != nil {
		slog.Error("模型事件发布失败", "reason", locales.Message{Code: "service.storage_failure", Args: map[string]any{}})
	}
}
