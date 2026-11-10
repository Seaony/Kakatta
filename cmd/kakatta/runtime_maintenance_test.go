package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/update"
)

type maintenanceForwarder struct {
	base   *http.Transport
	target *url.URL
	after  func(*http.Request, error)
}

func (f maintenanceForwarder) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme, copy.URL.Host = f.target.Scheme, f.target.Host
	response, err := f.base.RoundTrip(copy)
	if f.after != nil {
		f.after(request, err)
	}
	return response, err
}

type maintenanceUpdates struct {
	update.API
	reads, checks atomic.Int64
}

func (u *maintenanceUpdates) Read(ctx context.Context) (update.Status, error) {
	u.reads.Add(1)
	return u.API.Read(ctx)
}

func (u *maintenanceUpdates) CheckDue(ctx context.Context) error {
	u.checks.Add(1)
	return u.API.CheckDue(ctx)
}

func maintenanceFixture(t *testing.T, response http.HandlerFunc, after func(*http.Request, error)) (*runtimeGraph, *maintenanceUpdates, *atomic.Int64) {
	t.Helper()
	g := graphFixture(t)
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/repos/Seaony/Kakatta/releases/latest" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("更新请求带了凭据或使用错误路径")
		}
		if response != nil {
			response(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v1.2.3","html_url":"https://github.com/Seaony/Kakatta/releases/tag/v1.2.3","draft":false,"prerelease":false}`)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	g.updateTransport.CloseIdleConnections()
	g.updateTransport = &http.Transport{}
	checker, err := update.New(update.Dependencies{
		DB: g.database.DB(), Settings: g.preferences, CurrentVersion: "v1.2.2",
		HTTP: &http.Client{Transport: maintenanceForwarder{base: g.updateTransport, target: target, after: after}},
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := &maintenanceUpdates{API: checker}
	g.updates = observed
	return g, observed, &requests
}

func maintenancePrepare(t *testing.T, g *runtimeGraph) {
	t.Helper()
	if err := g.core.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func maintenanceStart(t *testing.T, g *runtimeGraph) {
	t.Helper()
	if err := g.start(); err != nil {
		t.Fatal(err)
	}
}

func maintenanceExec(t *testing.T, g *runtimeGraph, query string, args ...any) {
	t.Helper()
	if _, err := g.database.DB().ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func maintenanceSetting(t *testing.T, g *runtimeGraph, key, raw string) {
	t.Helper()
	if _, err := g.preferences.Apply(t.Context(), settings.Values{key: json.RawMessage(raw)}); err != nil {
		t.Fatal(err)
	}
}

func maintenanceClock(t *testing.T, g *runtimeGraph, state quota.ClockState, timezone string) string {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	zone, _ := json.Marshal(timezone)
	maintenanceExec(t, g, `UPDATE meta SET value=? WHERE key=?`, string(raw), appsettings.ClockKey)
	maintenanceExec(t, g, `INSERT INTO settings(key,value_json) VALUES('general.timezone',?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, string(zone))
	return string(raw)
}

func TestRuntimeMaintenanceWaitsWhenDisabledAndWakesAfterSettingsSave(t *testing.T) {
	g, observed, requests := maintenanceFixture(t, nil, nil)
	maintenanceSetting(t, g, "general.update_check", `false`)
	session := graphAdmin(t, g)
	events := graphEvents(t, g, session)
	if err := g.start(); err == nil || requests.Load() != 0 || observed.reads.Load() != 0 {
		t.Fatal("尚未准备就启动了维护或发请求", err)
	}
	maintenancePrepare(t, g)
	for range 100 {
		g.wakeMaintenance()
	}
	maintenanceStart(t, g)
	maintenanceStart(t, g)
	graphWait(t, func() bool { return observed.reads.Load() == 1 })
	time.Sleep(40 * time.Millisecond)
	if observed.reads.Load() != 1 || observed.checks.Load() != 0 || requests.Load() != 0 {
		t.Fatal("空闲维护发生了循环、重复启动或关闭开关时仍请求", observed.reads.Load(), observed.checks.Load(), requests.Load())
	}
	graphProvider(t, g, "http://127.0.0.1:1")
	graphWait(t, func() bool { return observed.reads.Load() >= 2 })
	if requests.Load() != 0 || observed.checks.Load() != 0 {
		t.Fatal("保存服务商唤醒了已关闭的更新请求")
	}
	response := graphRequest(t, g, session, http.MethodPatch, "/api/v1/settings", `{"values":{"general.update_check":true}}`)
	if response.Code != http.StatusOK {
		t.Fatal("真实设置保存失败", response.Code, response.Body.String())
	}
	graphEvent(t, events, "update.changed")
	status, err := g.updates.Read(t.Context())
	if err != nil || status.LastSuccessAt == nil || status.KnownRelease == nil || status.KnownRelease.Version != "v1.2.3" || requests.Load() != 1 {
		t.Fatal("开启设置没有唤醒真实更新检查", status, err, requests.Load())
	}
	if _, err := (graphSettings{g}).Apply(t.Context(), settings.Values{"general.update_check": json.RawMessage(`false`)}); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		g.wakeMaintenance()
	}
	time.Sleep(40 * time.Millisecond)
	if requests.Load() != 1 || observed.checks.Load() != 1 {
		t.Fatal("关闭开关或同期限内重复唤醒造成请求", requests.Load(), observed.checks.Load())
	}
}

func TestRuntimeMaintenanceUsesUpdateDeadlineWithoutPolling(t *testing.T) {
	g, observed, requests := maintenanceFixture(t, nil, nil)
	maintenancePrepare(t, g)
	at := time.Now().Add(450 * time.Millisecond)
	last := at.Add(-24 * time.Hour).UnixMilli()
	maintenanceExec(t, g, `INSERT INTO meta(key,value) VALUES('update.check.v1',?)`, fmt.Sprintf(`{"version":1,"last_attempt_at":%d}`, last))
	maintenanceStart(t, g)
	graphWait(t, func() bool { return observed.reads.Load() == 1 })
	time.Sleep(35 * time.Millisecond)
	if observed.reads.Load() != 1 || requests.Load() != 0 {
		t.Fatal("到期前没有只等计时器", observed.reads.Load(), requests.Load())
	}
	graphWait(t, func() bool { return requests.Load() == 1 })
	if time.Now().Before(at.Add(-time.Millisecond)) {
		t.Fatal("更新检查早于真实到期时间")
	}
	graphWait(t, func() bool {
		status, err := observed.API.Read(t.Context())
		return err == nil && status.LastSuccessAt != nil && observed.reads.Load() >= 3
	})
	before := observed.reads.Load()
	time.Sleep(40 * time.Millisecond)
	if observed.reads.Load() != before || requests.Load() != 1 {
		t.Fatal("完成更新后没有睡到下一期限", observed.reads.Load(), before, requests.Load())
	}
}

func TestRuntimeMaintenanceUpdateFailureKeepsNextDayAndDoesNotRetryOnWake(t *testing.T) {
	g, observed, requests := maintenanceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "172800")
		w.WriteHeader(http.StatusTooManyRequests)
	}, nil)
	maintenancePrepare(t, g)
	maintenanceStart(t, g)
	graphWait(t, func() bool {
		status, err := observed.API.Read(t.Context())
		return err == nil && status.Error != nil
	})
	status, err := observed.API.Read(t.Context())
	if err != nil || status.LastAttemptAt == nil || status.NextCheckAt == nil || *status.NextCheckAt < *status.LastAttemptAt+48*time.Hour.Milliseconds() {
		t.Fatal("更新限流没有保留更长的到期时间", status, err)
	}
	for range 100 {
		g.wakeMaintenance()
	}
	time.Sleep(40 * time.Millisecond)
	if requests.Load() != 1 || observed.checks.Load() != 1 {
		t.Fatal("失败后由变更事件触发了立即重试", requests.Load(), observed.checks.Load())
	}
}

func TestRuntimeMaintenanceProbeWaitsForProtectedWindowAndDeduplicatesUsageWake(t *testing.T) {
	g, _, updates := maintenanceFixture(t, nil, nil)
	maintenanceSetting(t, g, "general.update_check", `false`)
	var probes atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	provider := graphProvider(t, g, upstream.URL)
	maintenancePrepare(t, g)
	now := time.Now().UTC()
	start, _ := quota.DayBounds(now)
	oldStart, _ := quota.DayBounds(start.Add(-time.Second))
	expires := now.Add(450 * time.Millisecond)
	maintenanceExec(t, g, `UPDATE ai_providers SET error_state='{"code":"analysis.client.balance","args":{}}',error_at=? WHERE id=?`, oldStart.UnixMilli(), provider.ID)
	maintenanceExec(t, g, `INSERT INTO ai_calls(at,provider_id,purpose,ok,duration_ms) VALUES(?,?,'probe',0,1)`, start.Add(-time.Second).UnixMilli(), provider.ID)
	maintenanceClock(t, g, quota.ClockState{Version: 1, HighestUTC: now.UnixMilli(), Timezone: "UTC", Windows: []quota.DayWindow{{Start: oldStart.UnixMilli(), End: expires.UnixMilli()}}}, "UTC")
	maintenanceStart(t, g)
	time.Sleep(35 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatal("旧日保护窗口结束前重复探测")
	}
	graphWait(t, func() bool { return probes.Load() == 1 })
	if time.Now().Before(expires.Add(-time.Millisecond)) {
		t.Fatal("探测早于旧保护窗口结束")
	}
	graphWait(t, func() bool {
		return graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE provider_id=? AND purpose='probe'`, provider.ID) == 2
	})
	for range 100 {
		_ = g.publish("ai.usage.changed", nil, nil)
	}
	time.Sleep(40 * time.Millisecond)
	if probes.Load() != 1 || updates.Load() != 0 {
		t.Fatal("自己的用量事件重复探测或关闭开关仍检查更新", probes.Load(), updates.Load())
	}
}

func TestRuntimeMaintenanceSettingsWakeRetriesProbeOnlyAfterBudgetIsRaised(t *testing.T) {
	g, observed, updates := maintenanceFixture(t, nil, nil)
	maintenanceSetting(t, g, "general.update_check", `false`)
	var probes atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	provider := graphProvider(t, g, upstream.URL)
	maintenanceSetting(t, g, "ai.daily_token_limit", `1`)
	maintenancePrepare(t, g)
	now := time.Now().UTC()
	start, _ := quota.DayBounds(now)
	maintenanceExec(t, g, `UPDATE ai_providers SET error_state='{"code":"analysis.client.daily_quota","args":{}}',error_at=? WHERE id=?`, start.Add(-time.Second).UnixMilli(), provider.ID)
	maintenanceExec(t, g, `INSERT INTO ai_calls(at,purpose,ok,duration_ms,input_tokens,output_tokens) VALUES(?,'test',1,1,1,0)`, now.Add(-time.Millisecond).UnixMilli())
	maintenanceStart(t, g)
	graphWait(t, func() bool {
		return observed.reads.Load() >= 1 && graphScalar(t, g, `SELECT count(*) FROM system_events WHERE kind='ai_budget' AND resolved_at IS NULL`) == 1
	})
	if probes.Load() != 0 || graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) != 0 {
		t.Fatal("维护探测绕过每日token上限或消耗未发机会")
	}
	if _, err := (graphSettings{g}).Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`2`)}); err != nil {
		t.Fatal(err)
	}
	graphWait(t, func() bool { return probes.Load() == 1 })
	graphWait(t, func() bool { return graphScalar(t, g, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) == 1 })
	if updates.Load() != 0 {
		t.Fatal("调整模型额度触发了已关闭的更新请求")
	}
}

func TestRuntimeMaintenanceNextProbeWakeIsReadOnlyAcrossDayWindows(t *testing.T) {
	parse := func(raw string) time.Time {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	for _, test := range []struct {
		name, timezone, clockZone, observed, highest, want string
		windows                                            []quota.DayWindow
	}{
		{"UTC", "UTC", "UTC", "2025-01-02T12:00:00Z", "2025-01-02T12:00:00Z", "2025-01-03T00:00:00Z", nil},
		{"春季23小时", "America/New_York", "America/New_York", "2025-03-09T05:00:00Z", "2025-03-09T05:00:00Z", "2025-03-10T04:00:00Z", nil},
		{"秋季25小时", "America/New_York", "America/New_York", "2025-11-02T04:00:00Z", "2025-11-02T04:00:00Z", "2025-11-03T05:00:00Z", nil},
		{"午夜不存在", "America/Sao_Paulo", "America/Sao_Paulo", "2018-11-04T03:00:00Z", "2018-11-04T03:00:00Z", "2018-11-05T02:00:00Z", nil},
		{"旧窗更早结束", "America/Los_Angeles", "America/Los_Angeles", "2025-01-02T12:00:00Z", "2025-01-02T12:00:00Z", "2025-01-03T00:00:00Z", []quota.DayWindow{{Start: parse("2025-01-02T00:00:00Z").UnixMilli(), End: parse("2025-01-03T00:00:00Z").UnixMilli()}}},
		{"改时区不落库", "Asia/Shanghai", "UTC", "2025-01-02T12:00:00Z", "2025-01-02T12:00:00Z", "2025-01-02T16:00:00Z", nil},
		{"回拨仍等绝对期限", "UTC", "UTC", "2025-01-01T12:00:00Z", "2025-01-02T12:00:00Z", "2025-01-02T15:00:00Z", []quota.DayWindow{{Start: parse("2025-01-01T00:00:00Z").UnixMilli(), End: parse("2025-01-02T15:00:00Z").UnixMilli()}}},
		{"已过旧窗只在内存移除", "UTC", "UTC", "2025-01-02T12:00:00Z", "2025-01-02T12:00:00Z", "2025-01-03T00:00:00Z", []quota.DayWindow{{Start: parse("2025-01-01T00:00:00Z").UnixMilli(), End: parse("2025-01-02T11:00:00Z").UnixMilli()}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, _, _ := maintenanceFixture(t, nil, nil)
			provider := graphProvider(t, g, "http://127.0.0.1:1")
			maintenanceExec(t, g, `UPDATE ai_providers SET error_state='{"code":"analysis.client.balance","args":{"opaque":1e309}}',error_at=1 WHERE id=?`, provider.ID)
			windows := append([]quota.DayWindow{}, test.windows...)
			before := maintenanceClock(t, g, quota.ClockState{Version: 1, HighestUTC: parse(test.highest).UnixMilli(), Timezone: test.clockZone, Windows: windows}, test.timezone)
			maintenanceExec(t, g, `PRAGMA query_only=ON`)
			changes := graphScalar(t, g, `SELECT total_changes()`)
			next, err := g.nextProbeWake(t.Context(), parse(test.observed))
			if err != nil || next == nil || !next.Equal(parse(test.want)) || graphScalar(t, g, `SELECT total_changes()`) != changes {
				t.Fatal("日界、旧窗或只读规则错误", next, test.want, err)
			}
			var after string
			if err := g.database.DB().QueryRow(`SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&after); err != nil || before != after {
				t.Fatal("计划下一期限改写了持久保护时钟", err)
			}
		})
	}
}

func TestRuntimeMaintenanceDrainCancelsAndJoinsBeforeDatabaseClose(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	g, _, _ := maintenanceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}, func(request *http.Request, err error) {
		if err != nil && request.Context().Err() != nil {
			close(canceled)
			<-release
		}
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); g.cancel() })
	maintenancePrepare(t, g)
	maintenanceStart(t, g)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("更新检查没有进入本地假服务")
	}
	drained := make(chan error, 1)
	go func() { drained <- g.drain(context.Background()) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("排空没有取消更新请求")
	}
	select {
	case err := <-drained:
		t.Fatal("维护还未结束就报告已排空", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("维护结束后排空没有返回")
	}
	if err := g.database.DB().PingContext(t.Context()); err != nil || g.key == [32]byte{} {
		t.Fatal("排空提前关闭数据库或清密钥", err)
	}
	var clock string
	if err := g.database.DB().QueryRow(`SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&clock); err != nil || !strings.Contains(clock, `highest_utc`) {
		t.Fatal("排空后不能读取最终额度时钟", err)
	}
	if err := g.start(); err == nil || g.drain(t.Context()) != nil {
		t.Fatal("排空后又启动或重复排空失败", err)
	}
	if err := g.close(t.Context()); err != nil || g.database.DB().Ping() == nil || g.key != [32]byte{} {
		t.Fatal("最终关闭没有关闭数据库和清密钥", err)
	}
}

func TestRuntimeMaintenanceGraphCancellationReachesDetachedModelNotification(t *testing.T) {
	g := graphFixture(t)
	entered, canceled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(upstream.Close)
	graphChannel(t, g, upstream.URL)
	if err := g.recordLoginFailure(t.Context(), server.LoginFailureEvent{Active: true, Limit: 20, IP: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	eventID := graphScalar(t, g, `SELECT id FROM system_events WHERE kind='login_failures'`)
	ctx, stop := context.WithTimeout(context.WithoutCancel(g.ctx), 5*time.Second)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- g.notifyModelEvent(ctx, eventID) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("已持久化事件没有进入本地通知请求")
	}
	g.cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("剥离调用取消的通知未随运行代取消")
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("通知丢失图取消原因", err)
		}
	case <-time.After(time.Second):
		t.Fatal("图取消后通知未结束")
	}
	if graphScalar(t, g, `SELECT count(*) FROM deliveries WHERE event_id=? AND ok=0`, eventID) != 1 {
		t.Fatal("取消时没有保存已发通知的回执")
	}
}

func TestRuntimeMaintenanceStartAndDrainAreSerialized(t *testing.T) {
	for range 4 {
		g, _, requests := maintenanceFixture(t, nil, nil)
		maintenanceSetting(t, g, "general.update_check", `false`)
		maintenancePrepare(t, g)
		ready := make(chan struct{})
		var jobs sync.WaitGroup
		for range 8 {
			jobs.Go(func() { <-ready; _ = g.start() })
		}
		jobs.Go(func() {
			<-ready
			if err := g.drain(context.Background()); err != nil {
				t.Error(err)
			}
		})
		close(ready)
		jobs.Wait()
		if g.maintenanceDone != nil {
			select {
			case <-g.maintenanceDone:
			default:
				t.Fatal("排空返回后仍有维护循环")
			}
		}
		if err := g.start(); err == nil || requests.Load() != 0 {
			t.Fatal("启动关闭竞态留下请求或允许重启", err, requests.Load())
		}
	}
}
