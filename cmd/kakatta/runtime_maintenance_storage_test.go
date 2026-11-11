package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/update"
)

func maintenanceStorageStop(t *testing.T, g *runtimeGraph) {
	t.Helper()
	maintenanceExec(t, g, `CREATE TRIGGER maintenance_storage_clock BEFORE INSERT ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'local storage failure'); END`)
	t.Cleanup(func() { _, _ = g.database.DB().Exec(`DROP TRIGGER IF EXISTS maintenance_storage_clock`) })
	g.core.Wake()
	graphWait(t, func() bool { return g.core.RuntimeFailure() != nil })
	maintenanceExec(t, g, `DROP TRIGGER maintenance_storage_clock`)
	if g.ctx.Err() != nil {
		t.Fatal("存储闭锁取消了网页运行代")
	}
}

func TestMaintenanceStorageFailureStopsDueTimersAndDoesNotSpendProbe(t *testing.T) {
	g, observed, updates := maintenanceFixture(t, nil, nil)
	var probes atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(model.Close)
	provider := graphProvider(t, g, model.URL)
	maintenancePrepare(t, g)
	now := time.Now().UTC()
	start, _ := quota.DayBounds(now)
	oldStart, _ := quota.DayBounds(start.Add(-time.Second))
	due := now.Add(time.Second)
	maintenanceExec(t, g, `UPDATE ai_providers SET error_state='{"code":"analysis.client.balance","args":{}}',error_at=? WHERE id=?`, oldStart.UnixMilli(), provider.ID)
	maintenanceExec(t, g, `INSERT INTO ai_calls(at,provider_id,purpose,ok,duration_ms) VALUES(?,?,'probe',0,1)`, start.Add(-time.Second).UnixMilli(), provider.ID)
	maintenanceClock(t, g, quota.ClockState{Version: 1, HighestUTC: now.UnixMilli(), Timezone: "UTC", Windows: []quota.DayWindow{{Start: oldStart.UnixMilli(), End: due.UnixMilli()}}}, "UTC")
	maintenanceExec(t, g, `INSERT INTO meta(key,value) VALUES('update.check.v1',?)`, fmt.Sprintf(`{"version":1,"last_attempt_at":%d}`, due.Add(-24*time.Hour).UnixMilli()))
	maintenanceStart(t, g)
	graphWait(t, func() bool { return observed.reads.Load() == 1 })
	maintenanceStorageStop(t, g)
	beforeCalls := graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`)
	beforeWrites := graphScalar(t, g, `SELECT total_changes()`)
	for range 100 {
		g.wakeMaintenance()
	}
	<-time.After(max(time.Until(due.Add(100*time.Millisecond)), 0))
	if probes.Load() != 0 || updates.Load() != 0 || graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) != beforeCalls {
		t.Fatalf("停机后到期维护仍外发或消耗探测：模型%d，更新%d，探测记录%d→%d", probes.Load(), updates.Load(), beforeCalls, graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`))
	}
	if after := graphScalar(t, g, `SELECT total_changes()`); beforeWrites != after {
		t.Fatalf("停机维护仍写库：%d→%d", beforeWrites, after)
	}
	select {
	case <-g.maintenanceDone:
	default:
		t.Fatal("故障停机后的维护循环没有退出")
	}
}

type maintenanceReadBarrier struct {
	update.API
	entered, release chan struct{}
	once             sync.Once
}

func (gate *maintenanceReadBarrier) Read(ctx context.Context) (update.Status, error) {
	status, err := gate.API.Read(ctx)
	gate.once.Do(func() { close(gate.entered); <-gate.release })
	return status, err
}

func TestMaintenanceStorageFailureRecheckedAfterUpdateRead(t *testing.T) {
	g, _, requests := maintenanceFixture(t, nil, nil)
	maintenancePrepare(t, g)
	if err := g.core.Start(); err != nil {
		t.Fatal(err)
	}
	gate := &maintenanceReadBarrier{API: g.updates, entered: make(chan struct{}), release: make(chan struct{})}
	g.updates = gate
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
	finished := make(chan *time.Time, 1)
	go func() { finished <- g.maintain() }()
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("维护没有读取本地更新状态")
	}
	maintenanceStorageStop(t, g)
	release.Do(func() { close(gate.release) })
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("停机后维护被已结束的状态读取卡住")
	}
	if requests.Load() != 0 || graphScalar(t, g, `SELECT count(*) FROM meta WHERE key='update.check.v1'`) != 0 {
		t.Fatalf("状态读取途中停机，最后外发前没有重查：请求%d", requests.Load())
	}
}

func TestMaintenanceStorageFailureCancelsQueuedProbeAndRestartKeepsDailyRule(t *testing.T) {
	g, _, _ := maintenanceFixture(t, nil, nil)
	maintenanceSetting(t, g, "general.update_check", `false`)
	maintenanceSetting(t, g, "ai.max_concurrent_requests", `1`)
	var requests atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(model.Close)
	provider := graphProvider(t, g, model.URL)
	maintenancePrepare(t, g)
	if err := g.core.Start(); err != nil {
		t.Fatal(err)
	}
	today, _ := quota.DayBounds(time.Now().UTC())
	maintenanceExec(t, g, `UPDATE ai_providers SET error_state='{"code":"analysis.client.balance","args":{}}',error_at=? WHERE id=?`, today.Add(-time.Second).UnixMilli(), provider.ID)
	release, err := g.models.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var free sync.Once
	t.Cleanup(func() { free.Do(release) })
	beforeWrites := graphScalar(t, g, `SELECT total_changes()`)
	finished := make(chan *time.Time, 1)
	go func() { finished <- g.maintain() }()
	graphWait(t, func() bool { return graphScalar(t, g, `SELECT total_changes()`) > beforeWrites })
	maintenanceStorageStop(t, g)
	free.Do(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("停机后待模型名额的探测没有退出")
	}
	if requests.Load() != 0 || graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) != 0 {
		t.Fatal("停机后排队探测仍外发或消耗当天机会", requests.Load())
	}
	var sequence int
	var name, database string
	if err := g.database.DB().QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &database); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: filepath.Dir(database), Environment: settings.Environment{TZ: "UTC"}}
	paths, err := config.CreateDataDirectories(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	key, source := g.key, g.keySource
	if err := g.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := newRuntimeGraph(t.Context(), cfg, paths, key, source, "maintenance-restart")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restarted.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	maintenancePrepare(t, restarted)
	for range 3 {
		restarted.maintain()
	}
	if requests.Load() != 1 || graphScalar(t, restarted, `SELECT count(*) FROM ai_calls WHERE provider_id=? AND purpose='probe'`, provider.ID) != 1 {
		t.Fatal("同库重启后未发探测丢了机会，或当天重复消耗", requests.Load())
	}
}
