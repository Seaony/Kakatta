package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type formalBlockingClose struct {
	entered, release chan struct{}
	once             sync.Once
}

func (r *formalBlockingClose) Open(context.Context, service.ResourceSpec) (service.SessionResource, error) {
	return r, nil
}
func (r *formalBlockingClose) Runner() *web.Runner      { return nil }
func (r *formalBlockingClose) Chrome() *browser.Session { return nil }
func (r *formalBlockingClose) OpenRemote(context.Context) (*remote.Session, error) {
	return nil, errors.New("本测试不打开远程画面")
}
func (r *formalBlockingClose) Close() error {
	r.once.Do(func() { close(r.entered); <-r.release })
	return nil
}

func TestFormalReadyToRunWaitsForResourceCloseAndNewAccountGap(t *testing.T) {
	cfg, paths, key := graphConfig(t)
	resource := &formalBlockingClose{entered: make(chan struct{}), release: make(chan struct{})}
	g, err := newRuntimeGraphWithResources(t.Context(), cfg, paths, key, backup.KeyDefaultFile, "formal-close-test", resource)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.close(ctx); err != nil {
			t.Error(err)
		}
	})
	release := sync.OnceFunc(func() { close(resource.release) })
	t.Cleanup(release)
	if _, err := g.preferences.Apply(t.Context(), settings.Values{
		"crawl.quiet_hours":                    []byte(`{"enabled":false,"start":"00:00","end":"07:00"}`),
		"crawl.account_min_gap_seconds":        []byte(`2`),
		"crawl.account_min_gap_jitter_seconds": []byte(`0`),
	}); err != nil {
		t.Fatal(err)
	}
	oldEnded := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := g.database.DB().ExecContext(t.Context(), `INSERT INTO accounts(id,name,state,last_run_ended_at,next_gap_seconds) VALUES(1,'本地扫码账号','active',?,2)`, oldEnded); err != nil {
		t.Fatal(err)
	}
	if _, err := g.database.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,name,keyword,enabled,account_id,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'关闭回归','相机',0,1,1,30,'keyword','recommended',0)`); err != nil {
		t.Fatal(err)
	}
	graphStart(t, g)
	if _, err := g.core.RunNow(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resource.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("运行没有进入可控的资源关闭")
	}
	readyAt := func(now int64) bool {
		t.Helper()
		var ready int64
		if err := g.database.DB().QueryRowContext(t.Context(), formalReadyToRunQuery, now).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		return ready == 1
	}
	if graphScalar(t, g, `SELECT count(*) FROM run_records WHERE ended_at IS NOT NULL`) != 1 ||
		graphScalar(t, g, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) != 1 ||
		graphScalar(t, g, `SELECT last_run_ended_at FROM accounts WHERE id=1`) != oldEnded {
		t.Fatal("没有进入运行已结束但资源仍在关闭的窗口")
	}
	if readyAt(time.Now().UnixMilli()) {
		t.Error("资源仍在关闭，却按旧账号间隔放行了下一次运行")
	}
	release()
	graphWait(t, func() bool { return graphScalar(t, g, `SELECT count(*) FROM meta WHERE key='runtime.session.1'`) == 0 })
	newEnded := graphScalar(t, g, `SELECT last_run_ended_at FROM accounts WHERE id=1`)
	gap := graphScalar(t, g, `SELECT next_gap_seconds FROM accounts WHERE id=1`)
	if newEnded <= oldEnded || gap != 2 {
		t.Fatal("关闭完成后没有按正式逻辑结算新账号间隔")
	}
	deadline := newEnded + gap*1000
	if readyAt(deadline - 1) {
		t.Fatal("资源关闭后仍提前释放了新的账号间隔")
	}
	if !readyAt(deadline) {
		t.Fatal("资源关闭且新账号间隔到期后仍未允许运行")
	}
}
