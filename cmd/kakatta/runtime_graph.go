package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/buildinfo"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/imagecache"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/resources"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/update"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
	assets "github.com/Seaony/Kakatta/web"
)

func newRuntimeGraph(life context.Context, cfg config.Config, paths config.Paths, key [backup.MasterKeyBytes]byte, source backup.KeySource, generation string) (_ *runtimeGraph, err error) {
	defer clear(key[:])
	if err := life.Err(); err != nil {
		return nil, err
	}
	if generation == "" || source != backup.KeyDefaultFile && source != backup.KeyEnvironment && source != backup.KeyExternalFile {
		return nil, errors.New("运行代配置无效")
	}
	ctx, cancel := context.WithCancel(life)
	g := &runtimeGraph{generation: generation, key: key, keySource: source, ctx: ctx, cancel: cancel,
		maintenanceWake: make(chan struct{}, 1)}
	defer func() {
		if err != nil {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			if closeErr := g.close(cleanup); closeErr != nil {
				err = errors.Join(err, errRuntimeGraphNotDrained, closeErr)
			}
			stop()
			clear(g.key[:])
		}
	}()
	cipher, err := security.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	g.database, err = store.Open(ctx, paths.Database)
	if err != nil {
		return nil, err
	}
	g.preferences, err = appsettings.New(ctx, g.database.DB(), cfg.Environment, nil)
	if err != nil {
		return nil, err
	}
	g.authentication, g.bootstrap, err = auth.New(ctx, auth.Dependencies{DB: g.database.DB(), Cipher: cipher})
	if err != nil {
		return nil, err
	}
	factory, err := resources.New(web.Goofish())
	if err != nil {
		return nil, err
	}
	cache, err := imagecache.New(paths.Images)
	if err != nil {
		return nil, err
	}
	g.notifications, err = notification.New(notification.Dependencies{
		DB: g.database.DB(), Settings: g.preferences, Cipher: cipher, Publish: g.publish,
		VerificationHeldUntil: func(accountID, nowMS int64) *int64 {
			return g.core.VerificationHeldUntil(accountID, nowMS)
		},
	})
	if err != nil {
		return nil, err
	}
	g.models, err = modelservice.New(ctx, modelservice.Dependencies{
		DB: g.database.DB(), Settings: g.preferences, Cipher: cipher,
		Publish: g.publish, System: g.notifyModelEvent,
	})
	if err != nil {
		return nil, err
	}
	g.core, err = service.NewStopped(ctx, service.Dependencies{
		DB: g.database.DB(), Settings: g.preferences, Cipher: cipher, DataDir: paths.DataDir,
		ReadSnapshot: g.database.ReadSnapshot,
		Resources:    factory, Models: g.models, Notifier: g.notifications, ImageCache: cache,
		Publish: g.publish,
	})
	if err != nil {
		return nil, err
	}
	g.updateTransport = &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 1,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	g.updates, err = update.New(update.Dependencies{
		DB: g.database.DB(), Settings: g.preferences, CurrentVersion: buildinfo.Version,
		HTTP: &http.Client{Transport: g.updateTransport},
	})
	if err != nil {
		return nil, err
	}
	g.handler, err = server.New(server.Dependencies{
		Auth: g.authentication, Settings: graphSettings{g}, Health: g.database.DB().PingContext,
		Web: assets.Assets(), LoginFailure: g.recordLoginFailure,
	})
	if err != nil {
		return nil, err
	}
	if err = g.handler.RegisterService(g.core); err == nil {
		err = g.handler.RegisterNotifications(g.notifications)
	}
	if err == nil {
		err = g.handler.RegisterModels(g.models)
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (g *runtimeGraph) publish(kind string, reason *locales.Message, data json.RawMessage) error {
	if kind == "ai.providers.changed" || kind == "ai.usage.changed" {
		g.core.Wake()
	}
	if kind == "settings.changed" || kind == "ai.providers.changed" || kind == "ai.usage.changed" || kind == "proxy.changed" {
		g.wakeMaintenance()
	}
	return g.handler.Publish(server.Event{Type: kind, Reason: reason, Data: data})
}

func (g *runtimeGraph) notifyModelEvent(ctx context.Context, eventID int64) error {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.ctx, cancel)
	defer func() { stop(); cancel() }()
	return g.notifications.System(work, eventID)
}

func (g *runtimeGraph) recordLoginFailure(ctx context.Context, event server.LoginFailureEvent) error {
	events := eventstore.Store{DB: g.database.DB()}
	var changed bool
	var err error
	if event.Active {
		changed, err = events.Raise(ctx, "login_failures", 0, locales.Message{Code: "security.login_failures", Args: map[string]any{"count": event.Limit, "ip": event.IP}})
	} else {
		changed, err = events.Resolve(ctx, "login_failures", 0)
	}
	if err != nil {
		return err
	}
	if changed {
		logGraphPublish(g.publish("system_event.changed", nil, nil))
		g.core.Wake()
	}
	return nil
}

type graphSettings struct{ graph *runtimeGraph }

func (s graphSettings) Get(ctx context.Context) (settings.Snapshot, error) {
	return s.graph.preferences.Get(ctx)
}

func (s graphSettings) Definitions() []settings.Definition {
	return s.graph.preferences.Definitions()
}

func (s graphSettings) Apply(ctx context.Context, patch settings.Values) (settings.Snapshot, error) {
	result, err := s.graph.preferences.Apply(ctx, patch)
	if err != nil {
		return result, err
	}
	s.graph.core.SettingsChanged()
	logGraphPublish(s.graph.publish("settings.changed", nil, nil))
	return result, nil
}

func logGraphPublish(err error) {
	if err != nil {
		slog.Error("运行事件发布失败", "reason", locales.Message{Code: "service.storage_failure", Args: map[string]any{}})
	}
}

func (g *runtimeGraph) close(ctx context.Context) error {
	g.closeOnce.Do(func() {
		g.closeErr = g.drain(ctx)
		if g.closeErr != nil {
			return
		}
		if g.database != nil {
			g.closeErr = g.database.Close()
		}
		if g.closeErr == nil {
			clear(g.key[:])
			g.bootstrap = auth.Bootstrap{}
		}
	})
	return g.closeErr
}
