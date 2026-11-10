package modelservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type modelFixture struct {
	s         *Service
	db        *sql.DB
	settings  *appsettings.Store
	cipher    *security.Cipher
	now       atomic.Int64
	mu        sync.Mutex
	published []string
	notified  []int64
	onPublish func(string) error
	onSystem  func(context.Context, int64) error
}

func newModelFixture(t *testing.T) *modelFixture {
	t.Helper()
	f := &modelFixture{}
	f.now.Store(1_700_000_000_000)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.db = db.DB()
	t.Cleanup(func() { _ = db.Close() })
	f.settings, err = appsettings.New(t.Context(), f.db, settings.Environment{TZ: "UTC"}, f.time)
	if err != nil {
		t.Fatal(err)
	}
	f.cipher, err = security.NewCipher(security.NewMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	f.s, err = New(t.Context(), f.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := f.s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *modelFixture) time() time.Time { return time.UnixMilli(f.now.Load()).UTC() }

func (f *modelFixture) dependencies() Dependencies {
	return Dependencies{DB: f.db, Settings: f.settings, Cipher: f.cipher, Now: f.time,
		Publish: func(kind string, _ *locales.Message, _ json.RawMessage) error {
			f.mu.Lock()
			f.published = append(f.published, kind)
			f.mu.Unlock()
			if f.onPublish != nil {
				return f.onPublish(kind)
			}
			return nil
		}, System: func(ctx context.Context, id int64) error {
			f.mu.Lock()
			f.notified = append(f.notified, id)
			f.mu.Unlock()
			if f.onSystem != nil {
				return f.onSystem(ctx, id)
			}
			return nil
		}}
}

func modelInput() ProviderInput {
	return ProviderInput{Name: "本地样例", BaseURL: "http://127.0.0.1:11434/v1", VisionModel: "vision-model", Enabled: true}
}

func modelPointer[T any](v T) *T { return &v }

func (f *modelFixture) create(t *testing.T, input ProviderInput) ProviderView {
	t.Helper()
	result, err := f.s.CreateProvider(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	return result.Provider
}

func (f *modelFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func modelScalar[T any](t *testing.T, f *modelFixture, query string, args ...any) T {
	t.Helper()
	var value T
	if err := f.db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
