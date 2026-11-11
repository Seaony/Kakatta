package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
)

func deliveryService(t *testing.T, db *sql.DB, client *http.Client) *Service {
	t.Helper()
	values, err := appsettings.New(t.Context(), db, settings.Environment{TZ: "UTC"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := values.Apply(t.Context(), settings.Values{"general.language": json.RawMessage(`"zh-CN"`)}); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(security.NewMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Dependencies{DB: db, Settings: values, Cipher: cipher, Sender: notify.New(notify.Dependencies{HTTP: client})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.Close(ctx)
	})
	return s
}

func deliveryChannel(t *testing.T, s *Service, name, address string) channelIdentity {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"url": address})
	nameJSON, _ := json.Marshal(name)
	channel, err := s.CreateChannel(t.Context(), ChannelPatch{"type": json.RawMessage(`"webhook"`), "name": nameJSON, "config": json.RawMessage(`{"method":"POST","content_type":"JSON"}`), "secrets": raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO task_channels(task_id,channel_id) VALUES(1,?)`, channel.ID); err != nil {
		t.Fatal(err)
	}
	var result channelIdentity
	result.ID = channel.ID
	if err := s.db.QueryRowContext(t.Context(), `SELECT uuid FROM channels WHERE id=?`, channel.ID).Scan(&result.UUID); err != nil {
		t.Fatal(err)
	}
	return result
}

func deliveryAwait(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("等待本地投递超时")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFlushRunSuccessfulChannelDoesNotSkipQueuedSibling(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, nil)
	var callsA, callsB atomic.Int64
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { callsA.Add(1); w.WriteHeader(http.StatusOK) }))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer serverB.Close()
	deliveryChannel(t, s, "A", serverA.URL)
	channelB := deliveryChannel(t, s, "B", serverB.URL)
	turn, err := s.queues.acquire(t.Context(), channelB)
	if err != nil {
		t.Fatal(err)
	}
	type completed struct {
		result service.NotifyResult
		err    error
	}
	done := make(chan completed, 1)
	go func() {
		result, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, []int64{1})
		done <- completed{result, err}
	}()
	deliveryAwait(t, func() bool {
		return deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent'`) == 1
	})
	if callsB.Load() != 0 {
		t.Fatal("渠道B没有等待许可")
	}
	turn.finish(notify.Webhook, false, time.Now())
	got := <-done
	if got.err != nil || got.result.NotifiedCount != 1 || callsA.Load() != 1 || callsB.Load() != 1 {
		t.Fatalf("同轮部分成功错误：%+v %v A%d B%d", got.result, got.err, callsA.Load(), callsB.Load())
	}
	if _, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if callsA.Load() != 1 || callsB.Load() != 1 || deliveryCount(t, db, `SELECT failure_count FROM notification_intents WHERE id=1`) != 0 {
		t.Fatal("成功后补发失败渠道或累计全败")
	}
}

type deliveryTransport func(*http.Request) (*http.Response, error)

func (fn deliveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestFlushRunSavesExternalSuccessBeforeCallerCancellation(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int64
	s := deliveryService(t, db, &http.Client{Transport: deliveryTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		cancel()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: r}, nil
	})})
	deliveryChannel(t, s, "取消同时成功", "http://local.invalid/test")
	result, err := s.FlushRun(ctx, service.RunRef{TaskID: 1, SearchGeneration: 1}, []int64{1})
	if !errors.Is(err, context.Canceled) || result.NotifiedCount != 1 || deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at IS NOT NULL`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 || calls.Load() != 1 {
		t.Fatalf("取消抹掉成功：%+v %v %d", result, err, calls.Load())
	}
	if err := s.checkStorage(); err != nil {
		t.Fatalf("普通取消关闭了全局门禁：%v", err)
	}
}

func TestFlushRunStorageFailureBlocksResendAndCloseKeepsReceipt(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, nil)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	deliveryChannel(t, s, "本地", server.URL)
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_receipt BEFORE INSERT ON deliveries BEGIN SELECT RAISE(ABORT,'本地落盘失败'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
	if err == nil || result.NotifiedCount != 1 || s.checkStorage() == nil {
		t.Fatalf("未阻断存储故障：%+v %v", result, err)
	}
	if _, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil); err == nil {
		t.Fatal("闭锁后仍允许Flush")
	}
	if calls.Load() != 1 || deliveryCount(t, db, `SELECT failure_count FROM notification_intents WHERE id=1`) != 0 {
		t.Fatal("DB错误被记作网络失败或重发")
	}
	if _, err := db.ExecContext(t.Context(), `DROP TRIGGER fail_receipt`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	s.Close(ctx)
	if calls.Load() != 1 || deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent'`) != 1 {
		t.Fatal("关闭时未补记真实回执或额外发送")
	}
}

func TestFlushRunRebuildsSummaryAfterMemberDismissedInQueue(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 4, intentRecommended)
	s := deliveryService(t, db, nil)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"notify.max_per_run": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	body := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body <- string(raw)
		w.WriteHeader(200)
	}))
	defer server.Close()
	channel := deliveryChannel(t, s, "摘要", server.URL)
	turn, err := s.queues.acquire(t.Context(), channel)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
		done <- err
	}()
	deliveryAwait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.intentInFlight) == 4 })
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET is_dismissed=1 WHERE item_id='商品B'`); err != nil {
		t.Fatal(err)
	}
	turn.finish(notify.Webhook, false, time.Now())
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := <-body
	if strings.Contains(got, "商品B") || !strings.Contains(got, "商品A") || !strings.Contains(got, "商品C") || !strings.Contains(got, "商品D") {
		t.Fatalf("摘要未按当前成员重建：%s", got)
	}
	if deliveryCount(t, db, `SELECT count(*) FROM delivery_items`) != 3 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='sent'`) != 3 {
		t.Fatal("摘要关联包含被取消成员")
	}
}

func TestFlushRunCancelledInQueueHasNoAttempt(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, nil)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	channel := deliveryChannel(t, s, "等待", server.URL)
	turn, err := s.queues.acquire(t.Context(), channel)
	if err != nil {
		t.Fatal(err)
	}
	defer turn.finish(notify.Webhook, false, time.Now())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := s.FlushRun(ctx, service.RunRef{TaskID: 1, SearchGeneration: 1}, nil); done <- err }()
	deliveryAwait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.intentInFlight) == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("未响应取消：%v", err)
	}
	if calls.Load() != 0 || deliveryCount(t, db, `SELECT count(*) FROM deliveries`) != 0 || deliveryCount(t, db, `SELECT failure_count FROM notification_intents WHERE id=1`) != 0 {
		t.Fatal("等待取消产生了发送或失败次数")
	}
}

func TestFlushRunCallerDeadlineIsNotCompleteNetworkFailure(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, &http.Client{Transport: deliveryTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
	deliveryChannel(t, s, "本地超时", "http://local.invalid/timeout")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err := s.FlushRun(ctx, service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE ok=0`) != 1 || deliveryCount(t, db, `SELECT failure_count FROM notification_intents WHERE id=1`) != 0 {
		t.Fatalf("调用方超时被算成全败：%v", err)
	}
}

func TestFlushRunThreeRealFailuresNeverRetryInsideSender(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, nil)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	deliveryChannel(t, s, "三次失败", server.URL)
	for round := 0; round < 4; round++ {
		if _, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 3 || deliveryCount(t, db, `SELECT count(*) FROM deliveries`) != 3 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='abandoned' AND failure_count=3`) != 1 {
		t.Fatalf("隐式重发或未终止：%d", calls.Load())
	}
}

func TestFlushRunFormattingFailureIsBoundedWithoutFakeDelivery(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, nil)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	deliveryChannel(t, s, "本地格式失败", server.URL)
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET reason='{"code":"missing.unregistered_message","args":{}}'`); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		result, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
		if err != nil || len(result.Warnings) != 1 || result.Warnings[0].Code != "notification.delivery.invalid_message" {
			t.Fatalf("未返回安全格式失败：%+v %v", result, err)
		}
		if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE json_extract(reason,'$.code')='notification.delivery.invalid_message' AND json_extract(reason,'$.args.channel')='本地格式失败'`) != 1 {
			t.Fatal("无真实回执时丢失了安全的格式错误")
		}
	}
	if calls.Load() != 0 || deliveryCount(t, db, `SELECT count(*) FROM deliveries`) != 0 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE state='abandoned' AND failure_count=3`) != 1 {
		t.Fatal("格式失败伪造投递或没有有限重试")
	}
	if err := s.checkStorage(); err != nil {
		t.Fatalf("格式失败被误作存储故障：%v", err)
	}
}

func TestFlushRunPublishesCommittedFactsWithoutResendingOnStreamError(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, nil)
	var calls, events atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	deliveryChannel(t, s, "事件流", server.URL)
	s.publish = func(kind string, _ *locales.Message, raw json.RawMessage) error {
		if kind != "finding.changed" && kind != "channel.changed" {
			t.Errorf("未知事件：%s", kind)
		}
		if deliveryCount(t, db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 {
			t.Error("事务提交前发布了投递")
		}
		events.Add(1)
		return errors.New("本地事件流关闭")
	}
	result, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
	if err != nil || result.NotifiedCount != 1 || calls.Load() != 1 || events.Load() != 2 {
		t.Fatalf("流错误改变送达事实：%+v %v calls%d events%d", result, err, calls.Load(), events.Load())
	}
}

func TestFlushRunUnknownBudgetPriceSkipsWithSpecificReason(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentRecommended)
	s := deliveryService(t, db, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("预算价格未知时触发了外发")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	deliveryChannel(t, s, "未知价格预算", server.URL)
	if _, err := db.ExecContext(t.Context(), `UPDATE tasks SET budget_max=9500`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=NULL`); err != nil {
		t.Fatal(err)
	}
	_, err := s.FlushRun(t.Context(), service.RunRef{TaskID: 1, SearchGeneration: 1}, nil)
	if err != nil || s.checkStorage() != nil || deliveryCount(t, db, `SELECT count(*) FROM deliveries`) != 0 {
		t.Fatalf("未知价格仍外发或触发存储故障：%v", err)
	}
	var reason locales.Message
	var raw string
	if err := db.QueryRowContext(t.Context(), `SELECT notify_skip_reason FROM findings WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(raw), &reason) != nil || reason.Code != "notify.price_unknown_budget" {
		t.Fatalf("未知预算价格原因不准确：%s", raw)
	}
}

func TestFlushRunDropRechecksBudgetAfterChannelInterval(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentPriceDrop)
	s := deliveryService(t, db, nil)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	channel := deliveryChannel(t, s, "预算变更", server.URL)
	clock := newQueueTestClock()
	s.queues.startedAt, s.queues.now = clock.current().Add(-time.Hour), clock.current
	s.queues.waitFor = func(ctx context.Context, delay time.Duration) error {
		_, err := db.ExecContext(ctx, `UPDATE tasks SET budget_max=7000`)
		clock.advance(delay)
		return err
	}
	previous := mustQueueTurn(t, &s.queues, channel)
	previous.finish(notify.Feishu, true, clock.current())
	first := s.now().UnixMilli()
	planExec(t, db, `UPDATE notification_intents SET failure_count=1,first_failed_at=? WHERE id=1`, first)
	ref := service.RunRef{TaskID: 1, SearchGeneration: 1}
	result, err := s.FlushRun(t.Context(), ref, nil)
	if err != nil || result.NotifiedCount != 0 || calls.Load() != 0 || deliveryCount(t, db, `SELECT count(*) FROM deliveries`) != 0 {
		t.Fatalf("等待期间调低预算后仍向渠道发送：%+v %v，发送 %d 次", result, err, calls.Load())
	}
	if deliveryCount(t, db, `SELECT count(*) FROM tasks WHERE budget_max=7000`) != 1 ||
		deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='pending' AND failure_count=1 AND first_failed_at=? AND price=9000`, first) != 1 ||
		deliveryCount(t, db, `SELECT count(*) FROM findings WHERE recommended_price=10000 AND last_drop_notified_price IS NULL AND drop_notified_at IS NULL`) != 1 {
		t.Fatal("未发送时改写了失败窗口或降价基准，或未触发预算变化")
	}
	planExec(t, db, `UPDATE listings SET price=7000`)
	result, err = s.FlushRun(t.Context(), ref, nil)
	if err != nil || result.NotifiedCount != 1 || calls.Load() != 1 ||
		deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='sent' AND failure_count=1 AND first_failed_at=? AND price=7000`, first) != 1 ||
		deliveryCount(t, db, `SELECT count(*) FROM findings WHERE recommended_price=10000 AND last_drop_notified_price=7000 AND drop_notified_at IS NOT NULL`) != 1 {
		t.Fatalf("降到预算上限后没有结算原通知：%+v %v，发送 %d 次", result, err, calls.Load())
	}
}

func TestFlushRunPendingDropKeepsWindowWhenPriceFallsAgain(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentPriceDrop)
	s := deliveryService(t, db, nil)
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET notified_at=1`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	prices := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Price string `json:"price"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		select {
		case prices <- payload.Price:
		default:
			t.Error("发送次数超出预期")
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	deliveryChannel(t, s, "连续降价", server.URL)
	ref := service.RunRef{TaskID: 1, SearchGeneration: 1}
	if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
		t.Fatal(err)
	}
	first := deliveryCount(t, db, `SELECT first_failed_at FROM notification_intents WHERE id=1`)
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='pending' AND failure_count=1 AND price=9000`) != 1 {
		t.Fatal("九十元首次失败没有留下原意图")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=8500`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
		t.Fatal(err)
	}
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='pending' AND failure_count=2 AND first_failed_at=? AND price=8500`, first) != 1 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatal("再次降价换了意图或重置失败窗口")
	}
	if deliveryCount(t, db, `SELECT count(*) FROM findings WHERE last_drop_notified_price IS NULL AND drop_notified_at IS NULL`) != 1 {
		t.Fatal("失败提前推进了成功降价基准")
	}
	result, err := s.FlushRun(t.Context(), ref, nil)
	if err != nil || result.NotifiedCount != 1 {
		t.Fatalf("八十五元成功没有结算：%+v %v", result, err)
	}
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='sent' AND failure_count=2 AND first_failed_at=? AND price=8500`, first) != 1 || deliveryCount(t, db, `SELECT count(*) FROM findings WHERE notified_at=1 AND recommended_price=10000 AND last_drop_notified_price=8500 AND drop_notified_at IS NOT NULL`) != 1 {
		t.Fatal("最终成功没有沿原窗口更新降价基准")
	}
	if calls.Load() != 3 {
		t.Fatalf("发送次数=%d，预期三次", calls.Load())
	}
	for _, want := range []string{"¥90", "¥85", "¥85"} {
		if got := <-prices; got != want {
			t.Fatalf("实际发送价格=%s，预期%s", got, want)
		}
	}
}

func TestFlushRunAbandonedDropOnlyStartsNewWindowBelowAbandonedPrice(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentPriceDrop)
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET notified_at=1`); err != nil {
		t.Fatal(err)
	}
	s := deliveryService(t, db, nil)
	var clockMS atomic.Int64
	clockMS.Store(time.Now().UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(clockMS.Load()) }
	s.queues.now, s.queues.startedAt = s.now, s.now()
	var calls atomic.Int64
	var succeed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if succeed.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	deliveryChannel(t, s, "放弃后的降价", server.URL)
	ref := service.RunRef{TaskID: 1, SearchGeneration: 1}
	for round := 0; round < 3; round++ {
		if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
			t.Fatal(err)
		}
	}
	first := deliveryCount(t, db, `SELECT first_failed_at FROM notification_intents WHERE id=1`)
	if deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='abandoned' AND price=9000 AND failure_count=3`) != 1 {
		t.Fatal("原降价意图没有三次终止")
	}
	for _, price := range []int64{9000, 11000, 9500} {
		if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=?`, price); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 3 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents`) != 1 {
		t.Fatal("同价、回涨或回落到更高价重新打开了弃意图")
	}
	clockMS.Add(time.Hour.Milliseconds())
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=8500`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
		t.Fatal(err)
	}
	newID := deliveryCount(t, db, `SELECT id FROM notification_intents WHERE state='pending'`)
	newFirst := deliveryCount(t, db, `SELECT first_failed_at FROM notification_intents WHERE id=?`, newID)
	if newID <= 1 || newFirst != clockMS.Load() || newFirst == first || calls.Load() != 4 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND price=8500 AND failure_count=1 AND first_failed_at IS NOT NULL`, newID) != 1 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='abandoned' AND failure_count=3 AND first_failed_at=?`, first) != 1 {
		t.Fatal("更低价没有独立编号与失败窗口")
	}
	if deliveryCount(t, db, `SELECT count(*) FROM findings WHERE last_drop_notified_price IS NULL AND drop_notified_at IS NULL`) != 1 {
		t.Fatal("新窗口失败提前推进基准")
	}
	succeed.Store(true)
	if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=? AND state='sent' AND failure_count=1 AND first_failed_at=?`, newID, newFirst) != 1 || deliveryCount(t, db, `SELECT last_drop_notified_price FROM findings WHERE id=1`) != 8500 {
		t.Fatal("新降价窗口成功没有保留独立历史")
	}
}

func TestFlushRunDoesNotChangeDropPriceBetweenChannels(t *testing.T) {
	db := deliveryDB(t)
	deliverySeed(t, db, 1, intentPriceDrop)
	if _, err := db.ExecContext(t.Context(), `UPDATE findings SET notified_at=1`); err != nil {
		t.Fatal(err)
	}
	s := deliveryService(t, db, nil)
	var callsA, callsB atomic.Int64
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { callsA.Add(1); w.WriteHeader(http.StatusOK) }))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { callsB.Add(1); w.WriteHeader(http.StatusOK) }))
	defer serverB.Close()
	deliveryChannel(t, s, "A降价", serverA.URL)
	channelB := deliveryChannel(t, s, "B降价", serverB.URL)
	turn, err := s.queues.acquire(t.Context(), channelB)
	if err != nil {
		t.Fatal(err)
	}
	ref := service.RunRef{TaskID: 1, SearchGeneration: 1}
	done := make(chan error, 1)
	go func() { _, err := s.FlushRun(t.Context(), ref, nil); done <- err }()
	deliveryAwait(t, func() bool {
		return deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id=1 AND state='sent'`) == 1
	})
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET price=8500`); err != nil {
		t.Fatal(err)
	}
	turn.finish(notify.Webhook, false, time.Now())
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if callsA.Load() != 1 || callsB.Load() != 0 || deliveryCount(t, db, `SELECT price FROM notification_intents WHERE id=1`) != 9000 || deliveryCount(t, db, `SELECT last_drop_notified_price FROM findings WHERE id=1`) != 9000 {
		t.Fatal("同轮改写了已发出的价格或向另一渠道发送了新价格")
	}
	if _, err := s.FlushRun(t.Context(), ref, nil); err != nil {
		t.Fatal(err)
	}
	if callsA.Load() != 2 || callsB.Load() != 1 || deliveryCount(t, db, `SELECT count(*) FROM notification_intents WHERE id>1 AND state='sent' AND price=8500`) != 1 || deliveryCount(t, db, `SELECT last_drop_notified_price FROM findings WHERE id=1`) != 8500 {
		t.Fatal("下一轮没有将新降价作为独立提醒发送")
	}
}
