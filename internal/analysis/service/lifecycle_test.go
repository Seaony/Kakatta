package modelservice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func TestModelServiceConstructionRejectsMissingDependenciesAndCancellation(t *testing.T) {
	f := newModelFixture(t)
	for _, missing := range []func(*Dependencies){
		func(d *Dependencies) { d.DB = nil }, func(d *Dependencies) { d.Settings = nil },
		func(d *Dependencies) { d.Cipher = nil }, func(d *Dependencies) { d.Publish = nil }, func(d *Dependencies) { d.System = nil },
	} {
		deps := f.dependencies()
		missing(&deps)
		if s, err := New(t.Context(), deps); s != nil || err == nil {
			t.Fatal("依赖缺失仍构造成功")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := New(ctx, f.dependencies()); s != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("构造应保留取消：%v %v", s, err)
	}
	if _, err := f.s.ListProviders(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("操作没有保留调用方取消")
	}
}

func TestModelServiceCloseCancelsWaitsAndSupportsConcurrentRetry(t *testing.T) {
	f := newModelFixture(t)
	work, finish, err := f.s.beginJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := f.s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("未回收的工作不能被假报为已关闭：%v", err)
	}
	select {
	case <-work.Done():
	case <-time.After(time.Second):
		t.Fatal("关闭没有取消活动工作")
	}
	if _, err := f.s.ListProviders(context.Background()); err == nil {
		t.Fatal("关闭后仍接新工作")
	}
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- f.s.Close(context.Background()) }()
	}
	select {
	case <-done:
		t.Fatal("并发关闭没有等工作回收")
	default:
	}
	finish()
	finish()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("回收后并发关闭未完成")
		}
	}
}

func TestModelServiceCloseWaitsForCommittedNotificationReceipt(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	f.onSystem = func(ctx context.Context, id int64) error {
		if modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE id=?`, id) != 1 {
			t.Error("回调执行前事务未提交")
		}
		close(entered)
		<-release
		if ctx.Err() != nil {
			t.Error("已提交事件的回执被调用方取消抹掉")
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- f.s.ChangeProvider(context.Background(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("通知回调没有在提交后执行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := f.s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("回执没回收却返回关闭成功：%v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestModelServiceCallbackFailureIsSafeAndKeepsCommittedFacts(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	f.onPublish = func(string) error { return errors.New("callback-private-key") }
	f.onSystem = func(context.Context, int64) error {
		return errors.Join(closedService(), errors.New("callback-password"))
	}
	err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}})
	if err != nil || modelScalar[int](t, f, `SELECT count(*) FROM system_events`) != 1 || len(f.notified) != 1 {
		t.Fatalf("回调失败不能冒充已提交事实的存储失败：%v", err)
	}
}

func TestModelServiceConstructionNeverReinitializesMissingClock(t *testing.T) {
	f := newModelFixture(t)
	f.exec(t, `DELETE FROM meta WHERE key='quota.clock'`)
	if s, err := New(t.Context(), f.dependencies()); s != nil || err == nil {
		t.Fatal("已有设置服务的保护时钟缺失，不能按新应用重置")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM meta WHERE key='quota.clock'`) != 0 {
		t.Fatal("失败构造器重新初始化了时钟")
	}
}
