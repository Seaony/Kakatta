package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestServeUsesProvidedGraphFactory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := config.Config{DataDir: t.TempDir(), Listen: "127.0.0.1:0", Environment: settings.Environment{TZ: "UTC"}}
	prepareOfflineRuntime(t, cfg.DataDir, cfg.Environment)
	var calls atomic.Int64
	factory := func(ctx context.Context, cfg config.Config, paths config.Paths, key [backup.MasterKeyBytes]byte, source backup.KeySource, generation string) (*runtimeGraph, error) {
		defer clear(key[:])
		calls.Add(1)
		return newRuntimeGraph(ctx, cfg, paths, key, source, generation)
	}
	logs := &observedLog{address: make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- serveWithGraphFactory(ctx, cfg, func(string) (string, bool) { return "", false }, slog.New(slog.NewJSONHandler(logs, nil)), factory)
	}()
	select {
	case address := <-logs.address:
		response, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + address + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK || calls.Load() != 1 {
			t.Fatal("正式启动没有使用指定工厂或没有提供健康检查")
		}
	case err := <-done:
		t.Fatal("正式服务未启动", err)
	case <-time.After(5 * time.Second):
		t.Fatal("正式服务启动超时")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("正式服务没有干净退出")
	}
}

func TestRuntimeManagerReusesGraphFactoryWhenBuildingAnotherGeneration(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir(), Environment: settings.Environment{TZ: "UTC"}}
	paths := prepareOfflineRuntime(t, cfg.DataDir, cfg.Environment)
	key, source, keyFile, err := (runtimeKeyConfig{}).load(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key[:])
	var calls int
	factory := func(ctx context.Context, cfg config.Config, paths config.Paths, key [backup.MasterKeyBytes]byte, source backup.KeySource, generation string) (*runtimeGraph, error) {
		defer clear(key[:])
		calls++
		return newRuntimeGraph(ctx, cfg, paths, key, source, generation)
	}
	m, err := newRuntimeManagerWithGraphFactory(t.Context(), cfg, paths, key, source, keyFile, slog.New(slog.NewTextHandler(io.Discard, nil)), factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := m.pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.graph.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := m.buildGraph(key, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("重新装配运行代绕过了原工厂", calls)
	}
}
