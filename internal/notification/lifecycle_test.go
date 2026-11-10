package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func lifecycleService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "notifications.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(security.NewMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Dependencies{DB: db.DB(), Settings: preferences, Cipher: cipher})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s
}

func TestNotificationLifecycleRejectsMissingDependenciesAndPreservesQueueClock(t *testing.T) {
	if s, err := New(Dependencies{}); err == nil || s != nil {
		t.Fatal("缺少依赖仍构造通知服务")
	}
	s := lifecycleService(t)
	if s.sender == nil || s.queues.now == nil || s.queues.startedAt.IsZero() {
		t.Fatal("构造缺少唯一发送器或启动时钟")
	}
	if !s.queues.startedAt.Before(time.Now().Add(time.Second)) {
		t.Fatal("启动时间不符")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := s.beginJob(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("预取消仍接纳: %v", err)
	}
}

func TestNotificationCloseCancelsAndJoinsExistingJobs(t *testing.T) {
	s := lifecycleService(t)
	ctx, done, err := s.beginJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close(t.Context()) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("关闭未取消在途操作")
	}
	select {
	case err := <-closed:
		t.Fatalf("未join已返回: %v", err)
	default:
	}
	done()
	done()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.beginJob(t.Context()); err == nil {
		t.Fatal("关闭后仍接纳")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("重复关闭失败: %v", err)
	}
}

func TestChannelDeletionCancelsWaitersAndCanResumeAfterRollback(t *testing.T) {
	s := lifecycleService(t)
	identity := channelIdentity{ID: 7, UUID: "channel-seven"}
	ctx, done, err := s.beginChannel(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- s.stopChannel(t.Context(), identity) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("停用接纳未取消发送")
	}
	if _, _, err := s.beginChannel(t.Context(), identity); err == nil {
		t.Fatal("删除中仍接纳旧渠道")
	}
	otherCtx, otherDone, err := s.beginChannel(t.Context(), channelIdentity{ID: 8, UUID: "other"})
	if err != nil || otherCtx.Err() != nil {
		t.Fatal("连带阻断其他渠道", err)
	}
	otherDone()
	select {
	case <-stopped:
		t.Fatal("尚有发送便完成删除等待")
	default:
	}
	done()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	s.resumeChannel(identity)
	_, resumed, err := s.beginChannel(t.Context(), identity)
	if err != nil {
		t.Fatal("删除回滚未恢复", err)
	}
	resumed()
	if err := s.stopChannel(t.Context(), identity); err != nil {
		t.Fatal(err)
	}
	s.forgetChannel(identity)
	s.mu.Lock()
	remaining := len(s.channelJobs)
	s.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("已删除状态未回收或误删其他渠道: %d", remaining)
	}
}

func TestNotificationStorageGateSeparatesBusinessCancellationAndReceiptFailures(t *testing.T) {
	s := lifecycleService(t)
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, &service.ServiceError{Kind: service.ErrorConflict, Reason: locales.Message{Code: "findings.changed"}}} {
		if got := s.storageFailure(err); !errors.Is(got, err) {
			t.Fatalf("改写业务错误: %v", got)
		}
		if err := s.checkStorage(); err != nil {
			t.Fatalf("普通取消锁死通知: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var called atomic.Int32
	if err := s.saveReceipt(ctx, func(receiptCtx context.Context) error {
		called.Add(1)
		if receiptCtx.Err() != nil {
			t.Error("用户取消带入保存")
		}
		if deadline, ok := receiptCtx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			t.Error("无有界保存期限")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 1 {
		t.Fatal("隐式重复保存")
	}
	if err := s.saveReceipt(t.Context(), func(context.Context) error { called.Add(1); return context.DeadlineExceeded }); err == nil {
		t.Fatal("发送后保存超时被忽略")
	}
	if called.Load() != 2 {
		t.Fatal("保存失败立即隐式重试")
	}
	if _, _, err := s.beginJob(t.Context()); err == nil {
		t.Fatal("送达保存失败后仍接纳")
	}
	if err := s.checkStorage(); err == nil {
		t.Fatal("送达保存失败后仍出站")
	}
}

func TestNotificationCloseRetriesOnlyRetainedDatabaseReceiptOnce(t *testing.T) {
	s := lifecycleService(t)
	var calls atomic.Int32
	if err := s.saveReceipt(t.Context(), func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("private database failure")
		}
		return ctx.Err()
	}); err == nil {
		t.Fatal("第一次保存失败未返回")
	}
	if calls.Load() != 1 {
		t.Fatal("首次保存后自行重试")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal("补记后仍返回存储失败", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("补记次数错误: %d", calls.Load())
	}
	if err := s.Close(t.Context()); err != nil || calls.Load() != 2 {
		t.Fatal("重复关闭再次补记", err)
	}
	if _, _, err := s.beginJob(t.Context()); err == nil {
		t.Fatal("补记清门禁后错误重开已关闭服务")
	}
}

func TestDeliveryErrorsKeepOnlySafeTypedFields(t *testing.T) {
	for _, test := range []struct {
		result notify.Result
		code   string
		args   map[string]any
	}{
		{notify.Result{ErrorCode: "http_status", HTTPStatus: 401}, "http_status", map[string]any{"channel": "Telegram", "status": 401}},
		{notify.Result{ErrorCode: "provider_rejected", ProviderCode: "400"}, "provider_rejected", map[string]any{"channel": "Telegram", "code": "400"}},
		{notify.Result{ErrorCode: "provider_rejected", ProviderCode: "PRIVATE_SECRET"}, "provider_rejected_generic", map[string]any{"channel": "Telegram"}},
		{notify.Result{ErrorCode: "provider_rejected"}, "provider_rejected_generic", map[string]any{"channel": "Telegram"}},
		{notify.Result{ErrorCode: "PRIVATE_SECRET"}, "transport", map[string]any{"channel": "Telegram"}},
		{notify.Result{ErrorCode: "http_status", HTTPStatus: 0}, "transport", map[string]any{"channel": "Telegram"}},
	} {
		got := deliveryError("Telegram", test.result)
		if got.Code != "notification.delivery."+test.code || !reflect.DeepEqual(got.Args, test.args) {
			t.Fatalf("安全错误错误: %+v", got)
		}
		if text := fmt.Sprint(got); strings.Contains(text, "PRIVATE_SECRET") {
			t.Fatal("秘密回显", text)
		}
	}
	if got := deliveryError("Telegram", notify.Result{OK: true}); got != nil {
		t.Fatal("成功带错误")
	}
}

func TestNotificationFormatOptionsReadCurrentSettings(t *testing.T) {
	s := lifecycleService(t)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"general.language": json.RawMessage(`"en"`), "general.timezone": json.RawMessage(`"Asia/Tokyo"`), "general.public_url": json.RawMessage(`"https://example.invalid/base"`), "notify.max_per_run": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	options, maximum, err := s.loadFormatOptions(t.Context())
	if err != nil || options.Language != locales.En || options.Location.String() != "Asia/Tokyo" || options.PublicURL != "https://example.invalid/base" || maximum != 1 {
		t.Fatalf("读取快照错误: %+v %d %v", options, maximum, err)
	}
}

func TestNotificationQuiesceCancelsJoinsAndResumes(t *testing.T) {
	s := lifecycleService(t)
	ctx, done, err := s.beginJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	type quiesced struct {
		resume func()
		err    error
	}
	result := make(chan quiesced, 1)
	go func() { resume, err := s.Quiesce(t.Context()); result <- quiesced{resume, err} }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("维护未取消既有操作")
	}
	if _, _, err := s.beginJob(t.Context()); err == nil {
		t.Fatal("维护中仍接纳新操作")
	}
	select {
	case <-result:
		t.Fatal("既有操作未结束维护已返回")
	default:
	}
	done()
	got := <-result
	if got.err != nil || got.resume == nil {
		t.Fatalf("维护未成功静止: %v", got.err)
	}
	if err := s.checkStorage(); err == nil {
		t.Fatal("维护完成前放开出站")
	}
	got.resume()
	got.resume()
	fresh, end, err := s.beginJob(t.Context())
	if err != nil || fresh.Err() != nil {
		t.Fatalf("维护后没有恢复: %v", err)
	}
	end()
}

func TestNotificationQuiesceNestedResumeAndCloseStayDistinct(t *testing.T) {
	s := lifecycleService(t)
	first, err := s.Quiesce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Quiesce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first()
	if _, _, err := s.beginJob(t.Context()); err == nil {
		t.Fatal("另一维护尚在时提前恢复")
	}
	second()
	_, done, err := s.beginJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done()
	resume, err := s.Quiesce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	resume()
	if _, _, err := s.beginJob(t.Context()); err == nil {
		t.Fatal("维护恢复重开了已退出服务")
	}
}

func TestNotificationQuiesceTimeoutDoesNotLeaveWaiterOrPoisonNewContext(t *testing.T) {
	s := lifecycleService(t)
	old, oldDone, err := s.beginJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	resume, err := s.Quiesce(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || resume != nil || old.Err() == nil {
		t.Fatalf("超时维护状态不符: %v", err)
	}
	fresh, newDone, err := s.beginJob(t.Context())
	if err != nil || fresh.Err() != nil {
		t.Fatalf("失败维护没恢复接纳或沿旧取消: %v", err)
	}
	oldDone()
	newDone()
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationQuiesceRefusesUnsettledReceiptsAndKeepsFailureLatch(t *testing.T) {
	s := lifecycleService(t)
	var healthy atomic.Bool
	var calls atomic.Int32
	if err := s.saveReceipt(t.Context(), func(context.Context) error {
		calls.Add(1)
		if !healthy.Load() {
			return errors.New("未结算的真实通知回执")
		}
		return nil
	}); err == nil {
		t.Fatal("未返回保存失败")
	}
	if resume, err := s.Quiesce(t.Context()); err == nil || resume != nil {
		t.Fatal("未结算回执允许清除数据")
	}
	s.mu.Lock()
	pending, paused := len(s.receipts), s.paused
	s.mu.Unlock()
	if pending != 1 || paused != 0 || calls.Load() != 2 {
		t.Fatalf("维护失败丢回执或泄漏暂停: %d %d %d", pending, paused, calls.Load())
	}
	healthy.Store(true)
	resume, err := s.Quiesce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	pending = len(s.receipts)
	s.mu.Unlock()
	if pending != 0 || calls.Load() != 3 {
		t.Fatal("恢复后仍有旧回执穿过维护")
	}
	resume()
	if err := s.checkStorage(); err == nil {
		t.Fatal("清除数据恢复了存储故障门禁")
	}
}

func TestNotificationQuiesceSettlesAcceptedTestBeforeClearingHistory(t *testing.T) {
	s := lifecycleService(t)
	entered := make(chan struct{})
	s.sender = notify.New(notify.Dependencies{HTTP: &http.Client{Transport: deliveryTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})}})
	channel, err := s.CreateChannel(t.Context(), channelPatch(`{"type":"webhook","name":"维护前测试","secrets":{"url":"http://127.0.0.1/not-networked"}}`))
	if err != nil {
		t.Fatal(err)
	}
	type completed struct {
		result ChannelTestResult
		err    error
	}
	finished := make(chan completed, 1)
	go func() { result, err := s.TestChannel(t.Context(), channel.ID); finished <- completed{result, err} }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("测试尚未进入协议调用")
	}
	resume, err := s.Quiesce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	got := <-finished
	if got.err != nil || !got.result.OK || got.result.DeliveryID == 0 {
		t.Fatalf("维护丢弃已接受回执: %+v %v", got.result, got.err)
	}
	if count := channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries WHERE kind='test' AND ok=1`); count != 1 {
		t.Fatal("维护返回前未结算真实成功", count)
	}
	s.mu.Lock()
	active, receipts := s.activeJobs, len(s.receipts)
	s.mu.Unlock()
	if active != 0 || receipts != 0 {
		t.Fatalf("仍有旧操作或收据: %d %d", active, receipts)
	}
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM deliveries`); err != nil {
		t.Fatal(err)
	}
	resume()
	if count := channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries`); count != 0 {
		t.Fatal("旧测试回执重建了清除的历史", count)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if count := channelsCount(t, s.db, `SELECT COUNT(*) FROM deliveries`); count != 0 {
		t.Fatal("关闭时旧回执重建历史", count)
	}
}
