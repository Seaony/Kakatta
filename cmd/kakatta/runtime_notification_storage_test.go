package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/server"
)

func TestStorageFailureStopsModelAfterFromStartingNotification(t *testing.T) {
	g := graphFixture(t)
	var pushes, requests atomic.Int64
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(push.Close)
	graphChannel(t, g, push.URL)
	entered, release := make(chan struct{}), make(chan struct{})
	var free sync.Once
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid local key"}`)
	}))
	t.Cleanup(model.Close)
	t.Cleanup(func() { free.Do(func() { close(release) }) })
	graphProvider(t, g, model.URL)
	graphStart(t, g)
	finished := make(chan error, 1)
	go func() {
		_, err := g.models.GenerateProfile(t.Context(), modelservice.GenerateProfileInput{Keyword: "相机"})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("已发模型没有到达本地服务")
	}
	maintenanceStorageStop(t, g)
	free.Do(func() { close(release) })
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("本地模型401没有报告失败")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("已发模型没有完成原有记账")
	}
	if requests.Load() != 1 || graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='profile' AND ok=0`) != 1 || graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='ai_error'`) != 1 {
		t.Fatal("未经过真实模型HTTP、After记账和事件回调")
	}
	if pushes.Load() != 0 || graphScalar(t, g, `SELECT count(*) FROM deliveries`) != 0 || graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='ai_error' AND notified_at IS NOT NULL`) != 0 {
		t.Fatal("停机后模型After回调仍启动了新推送", pushes.Load())
	}
}

func TestStorageFailureCancelsAlreadySendingModelNotification(t *testing.T) {
	g := graphFixture(t)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var free sync.Once
	var requests atomic.Int64
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	t.Cleanup(push.Close)
	t.Cleanup(func() { free.Do(func() { close(release) }) })
	graphChannel(t, g, push.URL)
	if err := g.recordLoginFailure(t.Context(), server.LoginFailureEvent{Active: true, Limit: 20, IP: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	eventID := graphScalar(t, g, `SELECT id FROM system_events WHERE kind='login_failures'`)
	maintenancePrepare(t, g)
	ctx, stop := context.WithTimeout(context.WithoutCancel(g.ctx), 5*time.Second)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- g.notifyModelEvent(ctx, eventID) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("通知没有到达本地推送服务")
	}
	if err := g.core.Start(); err != nil {
		t.Fatal(err)
	}
	maintenanceStorageStop(t, g)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("全局闭锁没有取消已在途的通知HTTP")
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("通知丢失取消原因", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP取消后事件回调没有退出")
	}
	if requests.Load() != 1 || graphScalar(t, g, `SELECT count(*) FROM deliveries WHERE event_id=? AND ok=0`, eventID) != 1 {
		t.Fatal("取消通知没有保留唯一真实发送回执", requests.Load())
	}
}

func TestModelNotificationStillHonorsCallerDeadline(t *testing.T) {
	g := graphFixture(t)
	var requests atomic.Int64
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(push.Close)
	graphChannel(t, g, push.URL)
	if err := g.recordLoginFailure(t.Context(), server.LoginFailureEvent{Active: true, Limit: 20, IP: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	eventID := graphScalar(t, g, `SELECT id FROM system_events WHERE kind='login_failures'`)
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := g.notifyModelEvent(ctx, eventID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("合并核心寿命后丢失了调用方期限", err)
	}
	if requests.Load() != 1 || graphScalar(t, g, `SELECT count(*) FROM deliveries WHERE event_id=? AND ok=0`, eventID) != 1 || g.core.RuntimeContext().Err() != nil {
		t.Fatal("调用期限没有取消真实通知、没有保存回执或误停核心", requests.Load())
	}
}
