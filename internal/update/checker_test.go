package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type localTransport struct {
	target *url.URL
	base   *http.Transport
}

func (r *localTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme, copy.URL.Host = r.target.Scheme, r.target.Host
	return r.base.RoundTrip(copy)
}

type updateFixture struct {
	db       *sql.DB
	settings *appsettings.Store
	now      atomic.Int64
	calls    atomic.Int64
	checker  *Checker
	client   *http.Client
}

func newUpdateFixture(t *testing.T, response func(int64, http.ResponseWriter, *http.Request)) *updateFixture {
	t.Helper()
	f := &updateFixture{}
	f.now.Store(1_800_000_000_000)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.db = db.DB()
	t.Cleanup(func() { _ = db.Close() })
	f.settings, err = appsettings.New(t.Context(), f.db, settings.Environment{TZ: "UTC"}, f.time)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		if response == nil {
			writeRelease(w, "v1.2.4")
		} else {
			response(n, w, r)
		}
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	f.client = &http.Client{Transport: &localTransport{target: target, base: transport}}
	f.checker = f.newChecker(t, "1.2.3")
	return f
}

func (f *updateFixture) time() time.Time { return time.UnixMilli(f.now.Load()).UTC() }

func (f *updateFixture) newChecker(t *testing.T, version string) *Checker {
	t.Helper()
	c, err := New(Dependencies{DB: f.db, Settings: f.settings, CurrentVersion: version, Now: f.time, HTTP: f.client})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *updateFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *updateFixture) setEnabled(t *testing.T, enabled bool) {
	t.Helper()
	if _, err := f.settings.Apply(t.Context(), settings.Values{"general.update_check": json.RawMessage(fmt.Sprint(enabled))}); err != nil {
		t.Fatal(err)
	}
}

func (f *updateFixture) read(t *testing.T) Status {
	t.Helper()
	result, err := f.checker.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *updateFixture) raw(t *testing.T) string {
	t.Helper()
	var raw string
	err := f.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='update.check.v1'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeRelease(w http.ResponseWriter, version string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": version, "html_url": "https://github.com/Seaony/Kakatta/releases/tag/" + version, "draft": false, "prerelease": false, "body": "不保存远端正文", "new_upstream_field": true})
}

func TestUpdateRequiresExplicitDependencies(t *testing.T) {
	f := newUpdateFixture(t, nil)
	for _, change := range []func(*Dependencies){
		func(d *Dependencies) { d.DB = nil },
		func(d *Dependencies) { d.Settings = nil },
		func(d *Dependencies) { d.CurrentVersion = "" },
		func(d *Dependencies) { d.HTTP = nil },
		func(d *Dependencies) { d.HTTP = &http.Client{} },
	} {
		deps := Dependencies{DB: f.db, Settings: f.settings, CurrentVersion: "1.0.0", HTTP: f.client}
		change(&deps)
		if _, err := New(deps); err == nil {
			t.Fatal("缺依赖仍建立检查器")
		}
	}
	if f.calls.Load() != 0 || f.raw(t) != "" {
		t.Fatal("构造器不应检查或写入")
	}
}

func TestUpdateReadNeverWritesOrFetches(t *testing.T) {
	f := newUpdateFixture(t, nil)
	empty := f.read(t)
	if empty.CurrentVersion != "1.2.3" || !empty.Enabled || !empty.Stale || empty.LastAttemptAt != nil || empty.LastSuccessAt != nil || empty.KnownRelease != nil || empty.UpdateAvailable != nil || empty.NextCheckAt == nil || *empty.NextCheckAt != f.now.Load() {
		t.Fatalf("未检查的状态错误：%+v", empty)
	}
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := f.raw(t)
	for _, table := range []string{"meta", "settings", "system_events"} {
		for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
			f.exec(t, `CREATE TRIGGER no_update_read_`+table+action+` BEFORE `+action+` ON `+table+` BEGIN SELECT RAISE(ABORT,'read changed data'); END`)
		}
	}
	for range 3 {
		status := f.read(t)
		if status.UpdateAvailable == nil || !*status.UpdateAvailable || status.Stale || status.Error != nil || status.LastSuccessAt == nil || *status.LastSuccessAt != f.now.Load() {
			t.Fatalf("已有缓存的状态错误：%+v", status)
		}
	}
	if f.calls.Load() != 1 || before != f.raw(t) || strings.Contains(before, "不保存远端正文") || strings.Contains(before, "new_upstream_field") {
		t.Fatal("读取触网、写库或保存了原正文")
	}
	f.now.Add(24 * time.Hour.Milliseconds())
	if expired := f.read(t); !expired.Stale || expired.UpdateAvailable != nil || expired.KnownRelease == nil || expired.Error != nil || expired.NextCheckAt == nil || *expired.NextCheckAt != f.now.Load() {
		t.Fatalf("过期读取仍假称最新或丢掉历史：%+v", expired)
	}
	if f.calls.Load() != 1 || before != f.raw(t) {
		t.Fatal("过期读取触发了请求或改动缓存")
	}
}

func TestUpdateDailyAttemptSurvivesRestartAndToggle(t *testing.T) {
	f := newUpdateFixture(t, nil)
	f.setEnabled(t, false)
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 0 || f.raw(t) != "" || f.read(t).NextCheckAt != nil {
		t.Fatalf("禁用仍检查：%v", err)
	}
	f.setEnabled(t, true)
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := f.raw(t)
	f.setEnabled(t, false)
	if status := f.read(t); status.NextCheckAt != nil || status.UpdateAvailable != nil || status.KnownRelease == nil {
		t.Fatalf("禁用应停提示并保留历史：%+v", status)
	}
	f.setEnabled(t, true)
	f.checker = f.newChecker(t, "1.2.4")
	if status := f.read(t); status.UpdateAvailable == nil || *status.UpdateAvailable {
		t.Fatalf("升级后没有按运行版重新比较：%+v", status)
	}
	f.now.Add(24*time.Hour.Milliseconds() - 1)
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 1 || f.raw(t) != before {
		t.Fatalf("切开关/重启/时间边界提前请求：%v", err)
	}
	f.now.Add(1)
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 2 {
		t.Fatalf("到24小时没有检查：%v", err)
	}
	f.now.Add(4 * 24 * time.Hour.Milliseconds())
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 3 {
		t.Fatalf("多日停机不应补发：%v", err)
	}
}

func TestUpdateConditionalRequestsAndFailureKeepsGoodCache(t *testing.T) {
	f := newUpdateFixture(t, func(n int64, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/Seaony/Kakatta/releases/latest" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || r.UserAgent() != "Kakatta/1.2.3" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("请求不是固定匿名读取：%s %s %+v", r.Method, r.URL, r.Header)
		}
		if n > 1 && r.Header.Get("If-None-Match") != `W/"release-1"` {
			t.Errorf("弱etag未保留：%q", r.Header.Get("If-None-Match"))
		}
		switch n {
		case 1:
			w.Header().Set("ETag", `W/"release-1"`)
			writeRelease(w, "v1.2.4")
		case 2:
			w.Header().Set("ETag", `"bad-payload"`)
			_, _ = w.Write([]byte(`{"tag_name":"invalid","html_url":"private response marker"}`))
		default:
			w.WriteHeader(http.StatusNotModified)
		}
	})
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	successAt := *f.read(t).LastSuccessAt
	f.now.Add(24 * time.Hour.Milliseconds())
	if err := f.checker.CheckDue(t.Context()); err == nil {
		t.Fatal("坏响应没有报错")
	}
	failed := f.read(t)
	if failed.KnownRelease == nil || failed.KnownRelease.Version != "v1.2.4" || !failed.Stale || failed.UpdateAvailable != nil || failed.Error == nil || failed.Error.Code != "update.check_failed" || *failed.LastSuccessAt != successAt || *failed.LastAttemptAt != f.now.Load() || strings.Contains(f.raw(t), "private response marker") || strings.Contains(f.raw(t), "bad-payload") {
		t.Fatalf("失败覆盖了好缓存或伪称最新：%+v %s", failed, f.raw(t))
	}
	f.checker = f.newChecker(t, "1.2.3")
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 2 {
		t.Fatalf("失败后重启不应立即重试：%v", err)
	}
	f.now.Add(24 * time.Hour.Milliseconds())
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if status := f.read(t); status.Error != nil || status.Stale || status.UpdateAvailable == nil || !*status.UpdateAvailable || *status.LastSuccessAt != f.now.Load() {
		t.Fatalf("304没有确认现有缓存：%+v", status)
	}
}

func TestUpdateVersionsUseFullSemver(t *testing.T) {
	for _, test := range []struct {
		current, remote string
		available       *bool
	}{
		{"1.9.0", "v1.10.0", boolPointer(true)},
		{"v1.2.3+local", "v1.2.3+remote", boolPointer(false)},
		{"v2.0.0-rc.9", "v2.0.0", boolPointer(true)},
		{"v2.0.0", "v1.99.999", boolPointer(false)},
		{"dev", "v1.2.4", nil},
		{"abcdef01234", "v1.2.4", nil},
		{"v1.2", "v1.2.4", nil},
		{"v999999999999999999999999999999.0.0", "v999999999999999999999999999999.1.0", boolPointer(true)},
	} {
		t.Run(test.current+"/"+test.remote, func(t *testing.T) {
			f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, _ *http.Request) { writeRelease(w, test.remote) })
			f.checker = f.newChecker(t, test.current)
			if err := f.checker.CheckDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			if status := f.read(t); !reflect.DeepEqual(status.UpdateAvailable, test.available) || status.CurrentVersion != test.current {
				t.Fatalf("版本比较错误：%+v", status)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestUpdateParallelChecksReleaseDatabaseDuringHTTP(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			writeRelease(w, "v1.2.4")
		case <-r.Context().Done():
		}
	})
	done := make(chan error, 1)
	go func() { done <- f.checker.CheckDue(t.Context()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("请求未开始")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := f.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('fake.independent.write','yes')`); err != nil {
		t.Fatal("HTTP期间占住了数据库", err)
	}
	if status, err := f.checker.Read(ctx); err != nil || status.LastAttemptAt == nil || !status.Stale {
		t.Fatalf("HTTP期间不能读取已持久尝试：%+v %v", status, err)
	}
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			if err := f.checker.CheckDue(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if f.calls.Load() != 1 {
		t.Fatal("并发发了多次请求")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCancellationKeepsDurableAttempt(t *testing.T) {
	started := make(chan struct{})
	f := newUpdateFixture(t, func(_ int64, _ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.checker.CheckDue(ctx); !errors.Is(err, context.Canceled) || f.raw(t) != "" || f.calls.Load() != 0 {
		t.Fatalf("调用前取消仍写入或请求：%v", err)
	}
	if _, err := f.checker.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("读取没有保留取消：%v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.checker.CheckDue(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("请求未开始")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("请求中取消没有原样返回：%v", err)
	}
	status := f.read(t)
	if status.LastAttemptAt == nil || status.LastSuccessAt != nil || status.Error != nil || !status.Stale || status.UpdateAvailable != nil {
		t.Fatalf("正常停机被写成错误或丢掉尝试：%+v", status)
	}
	f.checker = f.newChecker(t, "1.2.3")
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 1 {
		t.Fatalf("取消后的重启绕过间隔：%v", err)
	}
}

func TestUpdateRateLimitsPersistLongerWait(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		headers map[string]string
		wait    time.Duration
	}{
		{"retry秒", 429, map[string]string{"Retry-After": "172800"}, 48 * time.Hour},
		{"retry日期", 503, map[string]string{"Retry-After": time.UnixMilli(1_800_000_000_000).UTC().Add(72 * time.Hour).Format(http.TimeFormat)}, 72 * time.Hour},
		{"主额度", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1800172800"}, 48 * time.Hour},
		{"poll", 200, map[string]string{"X-Poll-Interval": "172800"}, 48 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
				for name, value := range test.headers {
					w.Header().Set(name, value)
				}
				if test.status != 200 {
					w.WriteHeader(test.status)
					return
				}
				writeRelease(w, "v1.2.4")
			})
			initial := f.now.Load()
			err := f.checker.CheckDue(t.Context())
			if (err != nil) != (test.status != 200) {
				t.Fatalf("状态处理错误：%v", err)
			}
			status := f.read(t)
			if status.NextCheckAt == nil || *status.NextCheckAt != initial+test.wait.Milliseconds() {
				t.Fatalf("未尊重更长等待：%+v", status)
			}
			f.checker = f.newChecker(t, "1.2.3")
			f.now.Store(initial + test.wait.Milliseconds() - 1)
			if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 1 {
				t.Fatalf("限流前提前再试：%v", err)
			}
		})
	}
}

func TestUpdateRejectsBadResponse(t *testing.T) {
	valid := `{"tag_name":"v1.2.4","html_url":"https://github.com/Seaony/Kakatta/releases/tag/v1.2.4","draft":false,"prerelease":false}`
	for _, body := range []string{"null", "[]", "{}", valid + `{}`, strings.Replace(valid, "v1.2.4", "v1.2", 1), strings.Replace(valid, "v1.2.4", "v01.2.4", 1), strings.Replace(valid, "v1.2.4", "v1.2.4-rc.1", 1), strings.Replace(valid, `"draft":false`, `"draft":true`, 1), strings.Replace(valid, `"prerelease":false`, `"prerelease":true`, 1), strings.Replace(valid, "https://github.com/Seaony/Kakatta/", "https://evil.invalid/", 1), strings.Replace(valid, "https://github.com/", "http://github.com/", 1), strings.Replace(valid, "https://github.com/", "https://private@github.com/", 1), valid + strings.Repeat(" ", 1<<20)} {
		t.Run(fmt.Sprint(len(body))+"/"+body[:min(len(body), 45)], func(t *testing.T) {
			f := newUpdateFixture(t, func(_ int64, w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			if err := f.checker.CheckDue(t.Context()); err == nil {
				t.Fatal("坏响应仍成功")
			}
			status := f.read(t)
			if status.KnownRelease != nil || status.LastSuccessAt != nil || !status.Stale || status.Error == nil || status.UpdateAvailable != nil {
				t.Fatalf("坏响应被缓存为版本：%+v", status)
			}
		})
	}
}

func TestUpdateStorageErrorsNeverFetch(t *testing.T) {
	f := newUpdateFixture(t, nil)
	f.exec(t, `INSERT INTO meta(key,value) VALUES('update.check.v1','{}')`)
	for _, read := range []func() error{func() error { _, err := f.checker.Read(t.Context()); return err }, func() error { return f.checker.CheckDue(t.Context()) }} {
		var business *service.ServiceError
		if err := read(); !errors.As(err, &business) || business.Reason.Code != "service.storage_failure" {
			t.Fatalf("坏缓存没有失败：%v", err)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("坏存量仍发请求")
	}
}

func TestUpdateBackwardClockAndTimezoneDoNotResetAttempt(t *testing.T) {
	f := newUpdateFixture(t, nil)
	if err := f.checker.CheckDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, initial := f.raw(t), f.now.Load()
	f.now.Add(-time.Hour.Milliseconds())
	if _, err := f.settings.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"America/New_York"`)}); err != nil {
		t.Fatal(err)
	}
	f.checker = f.newChecker(t, "1.2.3")
	if err := f.checker.CheckDue(t.Context()); err != nil || f.calls.Load() != 1 || f.raw(t) != before {
		t.Fatalf("回拨/改时区恢复了检查机会：%v", err)
	}
	if status := f.read(t); status.NextCheckAt == nil || *status.NextCheckAt != initial+24*time.Hour.Milliseconds() {
		t.Fatalf("每天检查被换成时区自然日：%+v", status)
	}
}
