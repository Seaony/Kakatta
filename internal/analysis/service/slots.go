package modelservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Seaony/Kakatta/internal/analysis/client"
)

var _ client.Slots = (*Service)(nil)

type slotState struct {
	mu      sync.Mutex
	active  int64
	changed chan struct{}
}

func (s *Service) Acquire(ctx context.Context) (func(), error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return nil, err
	}
	for {
		if err := s.accountingFault(); err != nil {
			finish()
			return nil, err
		}
		limit, err := s.slotLimit(ctx)
		if err != nil {
			finish()
			return nil, storageFailure(err)
		}
		s.slots.mu.Lock()
		if s.slots.changed == nil {
			s.slots.changed = make(chan struct{})
		}
		if err := ctx.Err(); err != nil {
			s.slots.mu.Unlock()
			finish()
			return nil, err
		}
		if s.slots.active < limit {
			s.slots.active++
			s.slots.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					s.slots.mu.Lock()
					s.slots.active--
					close(s.slots.changed)
					s.slots.changed = make(chan struct{})
					s.slots.mu.Unlock()
					finish()
				})
			}, nil
		}
		changed := s.slots.changed
		s.slots.mu.Unlock()
		if err := s.accountingFault(); err != nil {
			finish()
			return nil, err
		}
		select {
		case <-ctx.Done():
			finish()
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (s *Service) slotLimit(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	var limit int64
	if err := json.Unmarshal(snapshot.Values["ai.max_concurrent_requests"], &limit); err != nil || limit < 1 {
		return 0, errors.New("模型并发上限无效")
	}
	return limit, nil
}
