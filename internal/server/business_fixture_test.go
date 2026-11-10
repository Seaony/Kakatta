package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/imagecache"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
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
