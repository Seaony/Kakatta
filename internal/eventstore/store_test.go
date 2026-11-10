package eventstore

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/store"
)

func TestEventDeduplicatesUntilResolved(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := Store{DB: db.DB(), Now: func() time.Time { return now }}
	message := locales.Message{Code: "security.login_failures", Args: map[string]any{"count": 20, "ip": "127.0.0.1"}}
	var group sync.WaitGroup
	var created atomic.Int64
	for range 20 {
		group.Go(func() {
			fresh, err := s.Raise(t.Context(), "login_failures", 0, message)
			if err != nil {
				t.Error(err)
			} else if fresh {
				created.Add(1)
			}
		})
	}
	group.Wait()
	if created.Load() != 1 {
		t.Fatalf("同一问题重复新建事件：%d", created.Load())
	}
	var raw string
	if err := db.DB().QueryRow(`SELECT message FROM system_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved locales.Message
	if err := json.Unmarshal([]byte(raw), &saved); err != nil || saved.Code != message.Code || saved.Args == nil {
		t.Fatalf("原因未按代码及参数保存：%s %v", raw, err)
	}
	if changed, err := s.Resolve(t.Context(), "login_failures", 0); err != nil || !changed {
		t.Fatalf("事件未解决：%v", err)
	}
	if changed, err := s.Resolve(t.Context(), "login_failures", 0); err != nil || changed {
		t.Fatalf("重复解决错误：%v", err)
	}
	if fresh, err := s.Raise(t.Context(), "login_failures", 0, message); err != nil || !fresh {
		t.Fatalf("新问题没有新建：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Raise(t.Context(), "login_failures", 0, message); err == nil {
		t.Fatal("存储失败被当成成功")
	}
}
