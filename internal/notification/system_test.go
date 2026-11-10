package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

type systemTransport func(*http.Request) (*http.Response, error)

func (f systemTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func systemReply(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type systemFixture struct {
	s        *Service
	settings *appsettings.Store
	clock    atomic.Int64
}

func newSystemFixture(t *testing.T, transport systemTransport) *systemFixture {
	t.Helper()
	f := &systemFixture{}
	f.clock.Store(time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC).UnixMilli())
	now := func() time.Time { return time.UnixMilli(f.clock.Load()) }
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, now)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(security.NewMasterKey())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := config.Apply(t.Context(), settings.Values{"general.language": json.RawMessage(`"zh-CN"`)}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if transport == nil {
		transport = func(*http.Request) (*http.Response, error) { return systemReply(`{}`), nil }
	}
	f.s, err = New(Dependencies{DB: db.DB(), Settings: config, Cipher: cipher, Now: now, Sender: notify.New(notify.Dependencies{HTTP: &http.Client{Transport: transport}})})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	f.settings = config
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = f.s.Close(ctx)
		db.Close()
	})
	return f
}

func systemExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func systemCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *systemFixture) channel(t *testing.T, id int64, kind notify.Type, enabled bool) channelIdentity {
	t.Helper()
	uuid := "system-test-" + strconv.FormatInt(id, 10)
	secrets, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1/notifications/" + strconv.FormatInt(id, 10) + "?token=must-not-appear"})
	sealed, err := f.s.cipher.Seal(secrets, "channels", "secret_enc", uuid)
	if err != nil {
		t.Fatal(err)
	}
	systemExec(t, f.s.db, `INSERT INTO channels(id,uuid,type,name,enabled,config_json,secret_enc) VALUES(?,?,?,?,?,'{}',?)`, id, uuid, kind, "渠道"+strconv.FormatInt(id, 10), enabled, sealed)
	return channelIdentity{ID: id, UUID: uuid}
}

func (f *systemFixture) event(t *testing.T, kind, raw string) int64 {
	t.Helper()
	subjectID := int64(0)
	switch kind {
	case "account_verify", "account_deny", "account_login", "ai_error", "task_failure_pause":
		subjectID = 9
	}
	result, err := f.s.db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES(?,?,?,?)`, kind, subjectID, raw, f.clock.Load())
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const systemLoginMessage = `{"code":"security.login_failures","args":{"count":20,"ip":"127.0.0.1"}}`

func TestSystemSelectsEnabledChannelsAndIgnoresCrawlQuietHours(t *testing.T) {
	for _, test := range []struct {
		name     string
		selected string
		want     int64
	}{
		{"空列表用全部启用渠道", `[]`, 2},
		{"显式列表只取启用交集", `[2,3]`, 1},
		{"没有启用交集", `[3]`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			f := newSystemFixture(t, func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				var payload map[string]string
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["kind"] != "system" || payload["urgent"] != "true" || payload["image"] != "" || payload["item_id"] != "" || payload["link"] != "" {
					t.Errorf("系统通知字段不符：%v", payload)
				}
				return systemReply(`{}`), nil
			})
			f.channel(t, 1, notify.Webhook, true)
			f.channel(t, 2, notify.Webhook, true)
			f.channel(t, 3, notify.Webhook, false)
			if _, err := f.settings.Apply(t.Context(), settings.Values{"notify.system_channel_ids": json.RawMessage(test.selected), "general.language": json.RawMessage(`"zh-CN"`)}); err != nil {
				t.Fatal(err)
			}
			id := f.event(t, "login_failures", systemLoginMessage)
			if err := f.s.System(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != test.want || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE kind='system' AND item_count=0 AND task_id IS NULL`) != test.want {
				t.Fatalf("实际请求/记录数量不符：%d", calls.Load())
			}
			if systemCount(t, f.s.db, `SELECT count(*) FROM delivery_items`) != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM notification_intents`) != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM delivery_intents`) != 0 {
				t.Fatal("系统通知混入了商品关联")
			}
			wantNotified := int64(0)
			if test.want > 0 {
				wantNotified = 1
			}
			if systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND notified_at IS NOT NULL`, id) != wantNotified {
				t.Fatal("未发送和已成功的事件标记不符")
			}
		})
	}
}

func TestSystemSingleFlightAndSameRoundChannelsContinueAfterSuccess(t *testing.T) {
	waiting, successSaved, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return systemReply(`{"errcode":0}`), nil
	})
	f.channel(t, 1, notify.Webhook, true)
	f.channel(t, 2, notify.WeCom, true)
	var once sync.Once
	f.s.publish = func(kind string, _ *locales.Message, _ json.RawMessage) error {
		if kind == "system_event.changed" {
			once.Do(func() { close(successSaved) })
		}
		return nil
	}
	f.s.queues.waitFor = func(ctx context.Context, delay time.Duration) error {
		close(waiting)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			f.clock.Add(delay.Milliseconds())
			return nil
		}
	}
	id := f.event(t, "login_failures", systemLoginMessage)
	finished := make(chan error, 1)
	go func() { finished <- f.s.System(t.Context(), id) }()
	<-waiting
	<-successSaved
	if calls.Load() != 1 {
		t.Fatalf("间隔结束前只应有一个渠道发送：%d", calls.Load())
	}
	if err := f.s.System(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := f.s.System(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 2 {
		t.Fatalf("同事件重复发送或同轮渠道被跳过：%d", calls.Load())
	}
}

func TestSystemResolvedDuringQueueWaitDoesNotSend(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return systemReply(`{"errcode":0}`), nil
	})
	f.channel(t, 1, notify.WeCom, true)
	f.s.queues.waitFor = func(ctx context.Context, delay time.Duration) error {
		close(entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			f.clock.Add(delay.Milliseconds())
			return nil
		}
	}
	id := f.event(t, "login_failures", systemLoginMessage)
	finished := make(chan error, 1)
	go func() { finished <- f.s.System(t.Context(), id) }()
	<-entered
	systemExec(t, f.s.db, `UPDATE system_events SET resolved_at=? WHERE id=?`, f.clock.Load(), id)
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries`) != 0 {
		t.Fatal("队列里已解决的事件仍发送了")
	}
}

func TestSystemOldReceiptCannotAcknowledgeNewEvent(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return systemReply(`{}`), nil
	})
	f.channel(t, 1, notify.Webhook, true)
	oldID := f.event(t, "login_failures", systemLoginMessage)
	finished := make(chan error, 1)
	go func() { finished <- f.s.System(t.Context(), oldID) }()
	<-entered
	systemExec(t, f.s.db, `UPDATE system_events SET resolved_at=? WHERE id=?`, f.clock.Load(), oldID)
	newID := f.event(t, "login_failures", systemLoginMessage)
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND notified_at IS NOT NULL`, oldID) != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND notified_at IS NOT NULL`, newID) != 0 {
		t.Fatal("旧请求结果没有准确归属原事件")
	}
	if systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE event_id=?`, oldID) != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE event_id=?`, newID) != 0 {
		t.Fatal("旧请求投递关联到了新事件")
	}
}

type systemCancelBody struct{ cancel context.CancelFunc }

func (b systemCancelBody) Read([]byte) (int, error) { b.cancel(); return 0, io.EOF }
func (systemCancelBody) Close() error               { return nil }

func TestSystemKnownSuccessIsSavedBeforeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: systemCancelBody{cancel}}, nil
	})
	f.channel(t, 1, notify.Webhook, true)
	id := f.event(t, "login_failures", systemLoginMessage)
	if err := f.s.System(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("应在持久化后返回取消：%v", err)
	}
	if systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND notified_at IS NOT NULL`, id) != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 {
		t.Fatal("取消丢掉了已接受的结果")
	}
}

func TestSystemNoDeliveryForCancellationBeforeSender(t *testing.T) {
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return systemReply(`{}`), nil
	})
	f.channel(t, 1, notify.Webhook, true)
	id := f.event(t, "login_failures", systemLoginMessage)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.s.System(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回取消：%v", err)
	}
	if calls.Load() != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries`) != 0 {
		t.Fatal("取消前未发送却留下了发送记录")
	}
}

func TestSystemAllFailuresKeepUnacknowledgedAndOnlyOneBatch(t *testing.T) {
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret-must-not-be-stored"))}, nil
	})
	f.channel(t, 1, notify.Webhook, true)
	id := f.event(t, "login_failures", systemLoginMessage)
	if err := f.s.System(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE notified_at IS NOT NULL`) != 0 {
		t.Fatal("失败不应伪造成功或自行重复尝试")
	}
	var raw string
	if err := f.s.db.QueryRowContext(t.Context(), `SELECT error FROM deliveries`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "must-not") || strings.Contains(raw, "127.0.0.1/notifications") || !strings.Contains(raw, "渠道1") {
		t.Fatalf("错误没有保留安全渠道说明：%s", raw)
	}
}

func TestSystemReceiptFailureStopsLaterOutboundAndCanSettleOnClose(t *testing.T) {
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return systemReply(`{}`), nil
	})
	f.channel(t, 1, notify.Webhook, true)
	id := f.event(t, "login_failures", systemLoginMessage)
	systemExec(t, f.s.db, `CREATE TRIGGER stop_system_receipt BEFORE INSERT ON deliveries BEGIN SELECT RAISE(ABORT,'模拟发送记录失败'); END`)
	if err := f.s.System(t.Context(), id); err == nil {
		t.Fatal("发送记录失败必须报错")
	}
	if err := f.s.System(t.Context(), id); err == nil {
		t.Fatal("存储故障应关闭后续出站")
	}
	if calls.Load() != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE notified_at IS NOT NULL`) != 0 {
		t.Fatal("记录失败后又发网络或伪写成功")
	}
	systemExec(t, f.s.db, `DROP TRIGGER stop_system_receipt`)
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE ok=1`) != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND notified_at IS NOT NULL`, id) != 1 || calls.Load() != 1 {
		t.Fatal("关闭时应只补原结果，不再发网络")
	}
}

func TestLoadSystemEventPreservesNumbersAndRejectsBrokenMessage(t *testing.T) {
	f := newSystemFixture(t, nil)
	for _, raw := range []string{`null`, `{"code":"security.login_failures","extra":1}`, `{"code":"security.login_failures"} {}`, `{"code":""}`, `{"args":{}}`} {
		id := f.event(t, "login_failures", raw)
		tx, err := f.s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.s.loadSystemEventTx(t.Context(), tx, id)
		tx.Rollback()
		if err == nil {
			t.Fatalf("损坏消息被接受：%s", raw)
		}
		systemExec(t, f.s.db, `DELETE FROM system_events WHERE id=?`, id)
	}
	id := f.event(t, "login_failures", `{"code":"security.login_failures","args":{"count":9007199254740993,"ip":"127.0.0.1"}}`)
	tx, err := f.s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event, err := f.s.loadSystemEventTx(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	if event.Message.Args["count"] != json.Number("9007199254740993") {
		t.Fatalf("事件数量失去精度：%v", event.Message.Args)
	}
}

func TestRecordSystemAttemptRollsBackWithMarkerAndNoItemAssociations(t *testing.T) {
	f := newSystemFixture(t, nil)
	id := f.event(t, "login_failures", systemLoginMessage)
	tx, err := f.s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	outcome := channelOutcome{ChannelID: 99, ChannelName: "原渠道名", Attempted: true, CalledSender: true, EndedAt: f.clock.Load(), Result: notify.Result{OK: true}}
	if _, err := f.s.recordSystemAttemptTx(t.Context(), tx, id, outcome); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if systemCount(t, f.s.db, `SELECT count(*) FROM deliveries`) != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE notified_at IS NOT NULL`) != 0 {
		t.Fatal("发送记录和成功标记没有一起回滚")
	}
}

func TestSystemFormatsPersistentFactsWithoutSubjectEntities(t *testing.T) {
	var body string
	var mu sync.Mutex
	f := newSystemFixture(t, func(request *http.Request) (*http.Response, error) {
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		mu.Lock()
		body = payload["body"]
		mu.Unlock()
		return systemReply(`{}`), nil
	})
	f.channel(t, 1, notify.Webhook, true)
	id := f.event(t, "task_failure_pause", `{"code":"run.failure_pause","args":{"task":"原任务名","count":7,"minutes":19,"reason":{"code":"run.timeout","args":{}}}}`)
	if err := f.s.System(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(body, "19") || !strings.Contains(body, "超时") {
		t.Fatalf("没有使用持久的失败事实：%s", body)
	}
}

func TestSystemBrowserHoldRequiresCurrentResourceEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		live    bool
		expired bool
		want    bool
	}{
		{"只有旧事件期限", false, false, false},
		{"当前资源仍活着", true, false, true},
		{"当前证明也已到期", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body string
			f := newSystemFixture(t, func(request *http.Request) (*http.Response, error) {
				var payload map[string]string
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				body = payload["body"]
				return systemReply(`{}`), nil
			})
			f.channel(t, 1, notify.Webhook, true)
			if test.live {
				f.s.verificationHeldUntil = func(accountID, nowMS int64) *int64 {
					if accountID != 9 {
						t.Errorf("账号编号不符：%d", accountID)
					}
					until := nowMS + time.Minute.Milliseconds()
					if test.expired {
						until = nowMS
					}
					return &until
				}
			}
			id := f.event(t, "account_verify", `{"code":"accounts.verification_required","args":{"name":"原账号","count":4,"held_until":9999999999999}}`)
			systemExec(t, f.s.db, `UPDATE system_events SET subject_id=9 WHERE id=?`, id)
			if err := f.s.System(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(body, "浏览器会保留到") != test.want {
				t.Fatalf("浏览器附句与活资源证明不符：%s", body)
			}
		})
	}
}

func TestSystemRechecksChannelAndTargetAfterQueueWait(t *testing.T) {
	for _, action := range []string{"禁用渠道", "删除渠道", "修改系统渠道选择"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int64
			f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return systemReply(`{"errcode":0}`), nil
			})
			f.channel(t, 1, notify.WeCom, true)
			f.channel(t, 2, notify.Webhook, false)
			if _, err := f.settings.Apply(t.Context(), settings.Values{"notify.system_channel_ids": json.RawMessage(`[1]`)}); err != nil {
				t.Fatal(err)
			}
			f.s.queues.waitFor = func(ctx context.Context, delay time.Duration) error {
				close(entered)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
					f.clock.Add(delay.Milliseconds())
					return nil
				}
			}
			id := f.event(t, "login_failures", systemLoginMessage)
			finished := make(chan error, 1)
			go func() { finished <- f.s.System(t.Context(), id) }()
			<-entered
			switch action {
			case "禁用渠道":
				systemExec(t, f.s.db, `UPDATE channels SET enabled=0 WHERE id=1`)
			case "删除渠道":
				systemExec(t, f.s.db, `DELETE FROM channels WHERE id=1`)
			default:
				if _, err := f.settings.Apply(t.Context(), settings.Values{"notify.system_channel_ids": json.RawMessage(`[2]`)}); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries`) != 0 {
				t.Fatal("发送用了排队前的旧渠道或选择")
			}
			if err := f.s.checkStorage(); err != nil {
				t.Fatalf("正常渠道变化不应关掉出站：%v", err)
			}
			if systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND failure_count=0 AND first_failed_at IS NULL`, id) != 1 {
				t.Fatal("渠道变化被算成整轮失败")
			}
		})
	}
}

func TestSystemPublishesCommittedReceiptWithoutSecretsAndIgnoresPublishFailure(t *testing.T) {
	for _, retryOnClose := range []bool{false, true} {
		name := "直接保存"
		if retryOnClose {
			name = "关闭时补记"
		}
		t.Run(name, func(t *testing.T) {
			f := newSystemFixture(t, nil)
			f.channel(t, 1, notify.Webhook, true)
			id := f.event(t, "login_failures", systemLoginMessage)
			var kinds []string
			f.s.publish = func(kind string, reason *locales.Message, data json.RawMessage) error {
				if systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE kind='system' AND ok=1`) != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND notified_at IS NOT NULL`, id) != 1 {
					t.Error("收据提交前发布了刷新事件")
				}
				if reason != nil || strings.Contains(string(data), "must-not-appear") || strings.Contains(string(data), "notifications") || strings.Contains(string(data), "token") {
					t.Errorf("刷新事件泄露了通知配置：%s", data)
				}
				var payload map[string]int64
				if err := json.Unmarshal(data, &payload); err != nil || len(payload) != 1 {
					t.Errorf("刷新数据不符：%s", data)
				}
				switch kind {
				case "system_event.changed":
					if payload["event_id"] != id {
						t.Errorf("事件编号不符：%s", data)
					}
				case "channel.changed":
					if payload["channel_id"] != 1 {
						t.Errorf("渠道编号不符：%s", data)
					}
				default:
					t.Errorf("用了未约定的刷新类型：%s", kind)
				}
				kinds = append(kinds, kind)
				return errors.New("模拟事件流已关闭")
			}
			if retryOnClose {
				systemExec(t, f.s.db, `CREATE TRIGGER stop_system_publish BEFORE INSERT ON deliveries BEGIN SELECT RAISE(ABORT,'模拟保存失败'); END`)
			}
			err := f.s.System(t.Context(), id)
			if retryOnClose {
				if err == nil || len(kinds) != 0 {
					t.Fatalf("保存失败时不应发布：%v，%v", err, kinds)
				}
				systemExec(t, f.s.db, `DROP TRIGGER stop_system_publish`)
				if err := f.s.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatalf("事件流失败不能改变已保存的通知结果：%v", err)
			}
			if len(kinds) != 2 || kinds[0] != "system_event.changed" || kinds[1] != "channel.changed" {
				t.Fatalf("收据提交后的刷新不完整：%v", kinds)
			}
		})
	}
}

func TestSystemRetryStopsAfterThreeWholeFailureRoundsAndLinksDeliveries(t *testing.T) {
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("私密响应"))}, nil
	})
	f.channel(t, 1, notify.Webhook, true)
	f.channel(t, 2, notify.Webhook, true)
	id := f.event(t, "login_failures", systemLoginMessage)
	firstAt := f.clock.Load()
	systemExec(t, f.s.db, `INSERT INTO deliveries(at,channel_id,kind,item_count,ok) VALUES(?,1,'system',0,0)`, firstAt-time.Hour.Milliseconds())
	for round := int64(1); round <= 4; round++ {
		if err := f.s.System(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		want := min(round, 3)
		if systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != want || calls.Load() != want*2 {
			t.Fatalf("第%d轮的计数或请求不符：%d", round, calls.Load())
		}
		if systemCount(t, f.s.db, `SELECT first_failed_at FROM system_events WHERE id=?`, id) != firstAt {
			t.Fatal("首次全败时间被旧无关联投递或后续轮次改写")
		}
		f.clock.Add(time.Minute.Milliseconds())
	}
	if systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE event_id=? AND kind='system' AND ok=0`, id) != 6 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND resolved_at IS NULL AND notified_at IS NULL`, id) != 1 {
		t.Fatal("事件关联不完整或重试终止伪造了解决/成功")
	}
	systemExec(t, f.s.db, `UPDATE system_events SET resolved_at=? WHERE id=?`, f.clock.Load(), id)
	newID := f.event(t, "login_failures", systemLoginMessage)
	if systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND failure_count=0 AND first_failed_at IS NULL`, newID) != 1 {
		t.Fatal("新事件沿用了旧事件的失败窗口")
	}
	if err := f.s.System(t.Context(), newID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 8 || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, newID) != 1 || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 3 {
		t.Fatal("新事件没有独立重试或影响了旧计数")
	}
}

func TestSystemRetryWindowIsStrictlyOverTwentyFourHours(t *testing.T) {
	for _, offset := range []int64{-1, 0, 1} {
		t.Run(strconv.FormatInt(offset, 10), func(t *testing.T) {
			var calls atomic.Int64
			f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			f.channel(t, 1, notify.Webhook, true)
			id := f.event(t, "login_failures", systemLoginMessage)
			first := f.clock.Load() - (24 * time.Hour).Milliseconds() - offset
			systemExec(t, f.s.db, `UPDATE system_events SET failure_count=1,first_failed_at=? WHERE id=?`, first, id)
			if err := f.s.System(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			want := int64(1)
			if offset > 0 {
				want = 0
			}
			if calls.Load() != want || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 1+want {
				t.Fatalf("24小时边界判断不符：%d", calls.Load())
			}
		})
	}
}

func TestSystemRetryRechecksWindowAfterChannelGap(t *testing.T) {
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return systemReply(`{"errcode":0}`), nil })
	f.channel(t, 1, notify.WeCom, true)
	f.s.queues.waitFor = func(ctx context.Context, delay time.Duration) error {
		f.clock.Add(delay.Milliseconds())
		return ctx.Err()
	}
	id := f.event(t, "login_failures", systemLoginMessage)
	systemExec(t, f.s.db, `UPDATE system_events SET failure_count=1,first_failed_at=? WHERE id=?`, f.clock.Load()-(24*time.Hour).Milliseconds(), id)
	if err := f.s.System(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries`) != 0 {
		t.Fatal("等渠道间隔时过期的事件仍发送了")
	}
}

func TestSystemRetryPartialSuccessNeverCountsAWholeFailure(t *testing.T) {
	var calls atomic.Int64
	f := newSystemFixture(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if strings.HasSuffix(request.URL.Path, "/1") {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return systemReply(`{}`), nil
	})
	f.channel(t, 1, notify.Webhook, true)
	f.channel(t, 2, notify.Webhook, true)
	id := f.event(t, "login_failures", systemLoginMessage)
	for range 2 {
		if err := f.s.System(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND first_failed_at IS NULL AND notified_at IS NOT NULL`, id) != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE event_id=?`, id) != 2 {
		t.Fatal("部分成功没有正确停止重发或关联投递")
	}
}

func TestSystemRetryCancelledPartialRoundDoesNotCount(t *testing.T) {
	waiting, recorded := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	f.channel(t, 1, notify.Webhook, true)
	f.channel(t, 2, notify.WeCom, true)
	f.s.queues.waitFor = func(ctx context.Context, _ time.Duration) error { close(waiting); <-ctx.Done(); return ctx.Err() }
	f.s.publish = func(kind string, _ *locales.Message, _ json.RawMessage) error {
		if kind == "channel.changed" {
			close(recorded)
		}
		return nil
	}
	id := f.event(t, "login_failures", systemLoginMessage)
	finished := make(chan error, 1)
	go func() { finished <- f.s.System(ctx, id) }()
	<-waiting
	<-recorded
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消结果不符：%v", err)
	}
	if systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE event_id=?`, id) != 1 {
		t.Fatal("部分取消被算成整轮失败")
	}
}

func TestSystemRetrySingleFlightCountsOneFailureRound(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-release
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	f.channel(t, 1, notify.Webhook, true)
	id := f.event(t, "login_failures", systemLoginMessage)
	finished := make(chan error, 1)
	go func() { finished <- f.s.System(t.Context(), id) }()
	<-entered
	if err := f.s.System(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 1 {
		t.Fatal("同事件并发重复发送或重复结算")
	}
}

func TestSystemRetryCanSettleFailedReceiptsAndRoundOnClose(t *testing.T) {
	for _, failAt := range []string{"receipt", "round"} {
		t.Run(failAt, func(t *testing.T) {
			var calls atomic.Int64
			f := newSystemFixture(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			f.channel(t, 1, notify.Webhook, true)
			id := f.event(t, "login_failures", systemLoginMessage)
			failedAt := f.clock.Load()
			trigger := `CREATE TRIGGER fail_system_retry BEFORE INSERT ON deliveries BEGIN SELECT RAISE(ABORT,'模拟收据失败'); END`
			if failAt == "round" {
				trigger = `CREATE TRIGGER fail_system_retry BEFORE UPDATE OF failure_count ON system_events BEGIN SELECT RAISE(ABORT,'模拟结算失败'); END`
			}
			systemExec(t, f.s.db, trigger)
			if err := f.s.System(t.Context(), id); err == nil {
				t.Fatal("保存失败必须关门")
			}
			if systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 0 {
				t.Fatal("未完成的保存已累计整轮失败")
			}
			systemExec(t, f.s.db, `DROP TRIGGER fail_system_retry`)
			f.clock.Add(time.Hour.Milliseconds())
			if err := f.s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := f.s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE event_id=?`, id) != 1 || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 1 || systemCount(t, f.s.db, `SELECT first_failed_at FROM system_events WHERE id=?`, id) != failedAt {
				t.Fatal("关闭补记重发、丢关联或重复累计全败")
			}
		})
	}
}

func TestSystemRetryInvalidConfigUsesBoundedAttemptsWithoutDeliveries(t *testing.T) {
	var calls atomic.Int64
	f := newSystemFixture(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return systemReply(`{}`), nil })
	identity := f.channel(t, 1, notify.Webhook, true)
	systemExec(t, f.s.db, `UPDATE channels SET config_json='{"unknown":true}' WHERE id=1`)
	id := f.event(t, "login_failures", systemLoginMessage)
	for range 4 {
		if err := f.s.System(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries`) != 0 || systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != 3 {
		t.Fatal("本地配置失败没有有限结算或伪造投递")
	}
	if lane := f.s.queues.lanes[identity]; lane != nil && !lane.lastFinishedAt.IsZero() {
		t.Fatal("没有调用发送器却更新了渠道间隔")
	}
}

func TestSystemRetryRejectsMissingFailureTimeAndBrokenFacts(t *testing.T) {
	for _, kind := range []string{"缺首次失败时间", "只有首次失败时间", "坏事件事实", "坏密文"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int64
			f := newSystemFixture(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return systemReply(`{}`), nil })
			f.channel(t, 1, notify.Webhook, true)
			id := f.event(t, "login_failures", systemLoginMessage)
			switch kind {
			case "缺首次失败时间":
				systemExec(t, f.s.db, `UPDATE system_events SET failure_count=1 WHERE id=?`, id)
			case "只有首次失败时间":
				systemExec(t, f.s.db, `UPDATE system_events SET first_failed_at=? WHERE id=?`, f.clock.Load(), id)
			case "坏事件事实":
				systemExec(t, f.s.db, `UPDATE system_events SET message='{"code":"security.login_failures","args":{}}' WHERE id=?`, id)
			case "坏密文":
				systemExec(t, f.s.db, `UPDATE channels SET secret_enc=x'00' WHERE id=1`)
			}
			if err := f.s.System(t.Context(), id); err == nil {
				t.Fatal("损坏状态被当成正常失败")
			}
			if err := f.s.checkStorage(); err == nil {
				t.Fatal("损坏状态没有关闭后续出站")
			}
			if calls.Load() != 0 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries`) != 0 {
				t.Fatal("损坏状态发了请求或伪造投递")
			}
		})
	}
}

func TestSystemCallerDeadlineIsNotCompleteNetworkFailure(t *testing.T) {
	for _, callerDeadline := range []bool{true, false} {
		name := "调用方截止"
		if !callerDeadline {
			name = "渠道超时且调用方仍有效"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			f := newSystemFixture(t, func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				if callerDeadline {
					<-request.Context().Done()
					return nil, request.Context().Err()
				}
				return nil, context.DeadlineExceeded
			})
			f.channel(t, 1, notify.Webhook, true)
			id := f.event(t, "login_failures", systemLoginMessage)
			ctx := t.Context()
			if callerDeadline {
				work, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				ctx = work
			}
			err := f.s.System(ctx, id)
			wantFailures := int64(1)
			if callerDeadline {
				wantFailures = 0
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("应返回调用方截止：%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || systemCount(t, f.s.db, `SELECT count(*) FROM deliveries WHERE event_id=? AND ok=0 AND json_extract(error,'$.code')='notification.delivery.timeout'`, id) != 1 {
				t.Fatal("没有保留真实渠道超时投递")
			}
			if systemCount(t, f.s.db, `SELECT failure_count FROM system_events WHERE id=?`, id) != wantFailures {
				t.Fatal("混淆了调用方截止和有效调用中的渠道超时")
			}
			if callerDeadline && systemCount(t, f.s.db, `SELECT count(*) FROM system_events WHERE id=? AND first_failed_at IS NULL AND notified_at IS NULL`, id) != 1 {
				t.Fatal("调用方截止开启了失败窗口或伪写成功")
			}
		})
	}
}
