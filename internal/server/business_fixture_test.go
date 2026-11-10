package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/imagecache"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/update"
)

type businessFixture struct {
	*serverFixture
	service    *service.Service
	dataDir    string
	cipher     *security.Cipher
	imageCache *imagecache.Cache
}

func newBusinessFixture(t *testing.T, factories ...service.ResourceFactory) *businessFixture {
	t.Helper()
	f := prepareBusinessFixture(t)
	f.start(t, failedBusinessNotifier{}, factories...)
	return f
}

func (f *businessFixture) registerTransferContracts(t *testing.T) {
	t.Helper()
	now := func() time.Time { return time.UnixMilli(f.clock.Load()) }
	if err := f.server.RegisterTransfers(f.service); err != nil {
		t.Fatal(err)
	}
	backups, err := backup.New(backup.Dependencies{
		DataDir: f.dataDir, AppVersion: "v1.0.0", SchemaVersion: store.SchemaVersion(), Now: now,
		Runtime: &backupHTTPRuntime{source: backup.SnapshotSource{
			Store: f.store, MasterKey: [32]byte(bytes.Repeat([]byte{4}, 32)), KeySource: backup.KeyDefaultFile, Generation: "contract",
		}},
		ValidateCandidate: func(ctx context.Context, path string, _ [backup.MasterKeyBytes]byte, version int) error {
			return store.ValidateBackup(ctx, path, version)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backups.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := f.server.RegisterBackup(backups); err != nil {
		t.Fatal(err)
	}
	updates, err := update.New(update.Dependencies{
		DB: f.store.DB(), Settings: f.settings.base, CurrentVersion: "v1.0.0", Now: now,
		HTTP: &http.Client{Transport: new(blockedNotificationTransport)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.server.RegisterUpdate(updates); err != nil {
		t.Fatal(err)
	}
}

func prepareBusinessFixture(t *testing.T) *businessFixture {
	t.Helper()
	f := &businessFixture{serverFixture: newServerFixture(t, true)}
	var sequence int
	var name, databasePath string
	if err := f.store.DB().QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &databasePath); err != nil || name != "main" {
		t.Fatal("无法取得临时主库目录")
	}
	f.dataDir = filepath.Dir(databasePath)
	var err error
	f.cipher, err = security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.imageCache, err = imagecache.New(filepath.Join(f.dataDir, "cache", "images"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *businessFixture) start(t *testing.T, notifier service.Notifier, factories ...service.ResourceFactory) {
	t.Helper()
	var resources service.ResourceFactory = failedBusinessResources{}
	if len(factories) != 0 {
		resources = factories[0]
	}
	var err error
	f.service, err = service.New(t.Context(), service.Dependencies{
		DB: f.store.DB(), Settings: f.settings.base, Cipher: f.cipher, DataDir: f.dataDir,
		Resources: resources, Models: failedBusinessModels{}, Notifier: notifier, ImageCache: f.imageCache,
		Now: func() time.Time { return time.UnixMilli(f.clock.Load()) },
		Publish: func(kind string, reason *locales.Message, data json.RawMessage) error {
			return f.server.Publish(Event{Type: kind, Reason: reason, Data: data})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.service.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := f.server.RegisterService(f.service); err != nil {
		t.Fatal(err)
	}
	notifications := registerNotificationHTTPFixture(t, f, nil)
	registerModelHTTPFixture(t, f, notifications)
}

type failedBusinessResources struct{}

func (failedBusinessResources) Open(context.Context, service.ResourceSpec) (service.SessionResource, error) {
	return nil, errors.New("本地测试未提供浏览器资源")
}

type failedBusinessModels struct{}

func (failedBusinessModels) Complete(context.Context, client.Request) (client.Result, error) {
	return client.Result{}, client.ErrNoProvider
}

type failedBusinessNotifier struct{}

func (failedBusinessNotifier) FlushRun(context.Context, service.RunRef, []int64) (service.NotifyResult, error) {
	return service.NotifyResult{}, errors.New("本地测试未提供发送渠道")
}

func (failedBusinessNotifier) System(context.Context, int64) error {
	return errors.New("本地测试未提供发送渠道")
}
