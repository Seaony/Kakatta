package modelservice

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func callStart(t *testing.T, f *modelFixture, purpose string) client.CallStart {
	t.Helper()
	p := f.create(t, modelInput())
	provider, err := f.s.Provider(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return client.CallStart{Provider: provider.Ref, Model: provider.VisionModel, Purpose: purpose, JSONMode: client.JSONSchema}
}

func callEnd(input, output int64) client.CallEnd {
	return client.CallEnd{OK: true, HTTPStatus: 200, Usage: client.Usage{Present: true, InputTokens: input, OutputTokens: output}, FinishReason: "stop", Duration: 123 * time.Millisecond}
}

func applyModelSettings(t *testing.T, f *modelFixture, values map[string]any) {
	t.Helper()
	patch := settings.Values{}
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		patch[key] = raw
	}
	if _, err := f.settings.Apply(t.Context(), patch); err != nil {
		t.Fatal(err)
	}
}

func beforeCall(t *testing.T, f *modelFixture, start client.CallStart) client.Ticket {
	t.Helper()
	ticket, err := f.s.Before(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestAccountingStoresEveryPhysicalCallAndKeepsStartSnapshot(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(time.Date(2025, 1, 1, 23, 59, 0, 0, time.UTC).UnixMilli())
	start := callStart(t, f, "analysis")
	id := int64(42)
	start.TaskID = &id
	at := f.now.Load()
	ticket := beforeCall(t, f, start)
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 {
		t.Fatal("发请求前不能预先写调用记录")
	}
	id = 99
	f.now.Add(int64(2 * time.Minute / time.Millisecond))
	end := callEnd(7, 3)
	end.Usage.ReasoningTokens = modelPointer(int64(2))
	if err := f.s.After(t.Context(), ticket, end); err != nil {
		t.Fatal(err)
	}
	var gotAt, task, input, output, reasoning, duration int64
	var purpose, mode, finish string
	var status int
	var ok bool
	if err := f.db.QueryRowContext(t.Context(), `SELECT at,task_id,input_tokens,output_tokens,reasoning_tokens,duration_ms,purpose,json_mode,finish_reason,http_status,ok FROM ai_calls`).Scan(&gotAt, &task, &input, &output, &reasoning, &duration, &purpose, &mode, &finish, &status, &ok); err != nil {
		t.Fatal(err)
	}
	if gotAt != at || task != 42 || input+output != 10 || reasoning != 2 || duration != 123 || purpose != "analysis" || mode != "schema" || finish != "stop" || status != 200 || !ok {
		t.Fatalf("调用事实或开始快照错误：%d %d %d %d %d %d %s %s %s %d %v", gotAt, task, input, output, reasoning, duration, purpose, mode, finish, status, ok)
	}
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 1})
	// 跨日完成仍属于发起日，今天的新请求可继续。
	start.TaskID = nil
	ticket = beforeCall(t, f, start)
	if err := f.s.DeleteProvider(t.Context(), start.Provider.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.After(t.Context(), ticket, client.CallEnd{ErrorKind: "network", Duration: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if !modelScalar[bool](t, f, `SELECT input_tokens IS NULL AND output_tokens IS NULL AND reasoning_tokens IS NULL AND http_status IS NULL AND finish_reason IS NULL AND task_id IS NULL FROM ai_calls ORDER BY id DESC LIMIT 1`) || modelScalar[string](t, f, `SELECT error_kind FROM ai_calls ORDER BY id DESC LIMIT 1`) != "network" {
		t.Fatal("缺失用量应保留NULL，删除服务商不能丢掉在途调用")
	}
}

func TestAccountingBudgetIsRaisedOnLastCallAndOnlyTestBypasses(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 10})
	start := callStart(t, f, "market")
	f.onSystem = func(ctx context.Context, id int64) error {
		if modelScalar[int64](t, f, `SELECT input_tokens+output_tokens FROM ai_calls ORDER BY id DESC LIMIT 1`) != 10 || modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE id=?`, id) != 1 {
			t.Error("额度通知必须发生在用量与事件提交之后")
		}
		return nil
	}
	if err := f.s.After(t.Context(), beforeCall(t, f, start), callEnd(7, 3)); err != nil {
		t.Fatal(err)
	}
	var msg locales.Message
	if err := json.Unmarshal([]byte(modelScalar[string](t, f, `SELECT message FROM system_events WHERE kind='ai_budget'`)), &msg); err != nil || msg.Code != "run.ai_daily_limit" || msg.Args["tokens"] != float64(10) {
		t.Fatalf("事件必须持久化实际用量：%+v %v", msg, err)
	}
	f.onSystem = nil
	for _, purpose := range []string{"title_screen", "analysis", "profile", "market", "probe"} {
		start.Purpose = purpose
		if _, err := f.s.Before(t.Context(), start); !errors.Is(err, client.ErrDailyLimit) {
			t.Fatalf("%s 绕过每日上限：%v", purpose, err)
		}
	}
	start.Purpose = "test"
	if err := f.s.After(t.Context(), beforeCall(t, f, start), callEnd(1, 1)); err != nil {
		t.Fatal(err)
	}
	if len(f.notified) != 1 || modelScalar[int](t, f, `SELECT count(*) FROM system_events`) != 1 {
		t.Fatal("同一额度事件不能重复创建或通知")
	}
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 11})
	start.Purpose = "probe"
	if _, err := f.s.Before(t.Context(), start); !errors.Is(err, client.ErrDailyLimit) {
		t.Fatal("调高后仍不足不能恢复请求")
	}
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 13})
	ticket := beforeCall(t, f, start)
	if modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE kind='ai_budget' AND resolved_at IS NOT NULL`) != 1 {
		t.Fatal("额度恢复没有解决旧事件")
	}
	if err := f.s.After(t.Context(), ticket, client.CallEnd{}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountingClockRollbackAndTimezoneCannotRestoreBudget(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(time.Date(2025, 1, 2, 0, 30, 0, 0, time.UTC).UnixMilli())
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 5})
	start := callStart(t, f, "market")
	if err := f.s.After(t.Context(), beforeCall(t, f, start), callEnd(2, 3)); err != nil {
		t.Fatal(err)
	}
	f.now.Add(-int64(2 * time.Hour / time.Millisecond))
	if _, err := f.s.Before(t.Context(), start); !errors.Is(err, client.ErrDailyLimit) {
		t.Fatalf("回拨恢复了额度：%v", err)
	}
	applyModelSettings(t, f, map[string]any{"general.timezone": "America/Los_Angeles"})
	if _, err := f.s.Before(t.Context(), start); !errors.Is(err, client.ErrDailyLimit) {
		t.Fatalf("切时区恢复了保护窗口额度：%v", err)
	}
	f.now.Store(time.Date(2025, 1, 3, 9, 0, 0, 0, time.UTC).UnixMilli())
	if err := f.s.After(t.Context(), beforeCall(t, f, start), client.CallEnd{}); err != nil {
		t.Fatal(err)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE resolved_at IS NULL`) != 0 {
		t.Fatal("次日没有恢复额度")
	}
}

func TestAccountingBeforeDependencyErrorsDoNotCreateTicketsOrLatch(t *testing.T) {
	for _, query := range []string{
		`INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(NULL,1,0)`,
		`INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(1700000000000,-1,0)`,
		`INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(1700000000000,9223372036854775807,1)`,
		`INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(1700000000000,9223372036854775807,0),(1700000000000,1,0)`,
		`INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(1700000000001,0,0)`,
	} {
		t.Run(query, func(t *testing.T) {
			f := newModelFixture(t)
			start := callStart(t, f, "market")
			f.exec(t, query)
			if _, err := f.s.Before(t.Context(), start); err == nil {
				t.Fatal("损坏用量没有关闭闸门")
			}
			if len(f.s.accounting.tickets) != 0 || f.s.accountingFault() != nil {
				t.Fatal("发出之前的失败不应创造票据或永久闭锁")
			}
			f.exec(t, `DELETE FROM ai_calls`)
			if err := f.s.After(t.Context(), beforeCall(t, f, start), client.CallEnd{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	f := newModelFixture(t)
	start := callStart(t, f, "market")
	f.exec(t, `CREATE TRIGGER fail_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'local failure'); END`)
	if _, err := f.s.Before(t.Context(), start); err == nil {
		t.Fatal("保存保护时钟失败还发票据")
	}
	f.exec(t, `DROP TRIGGER fail_clock`)
	if _, err := f.s.SetProviderEnabled(t.Context(), start.Provider.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Before(t.Context(), start); err != client.ErrConfigChanged {
		t.Fatalf("旧配置竟拿到新票据：%v", err)
	}
}

func TestAccountingWriteFailureLatchesButOtherTicketsStillFinish(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_accounting BEFORE INSERT ON ai_calls BEGIN SELECT RAISE(ABORT,'private detail'); END`,
		`CREATE TRIGGER fail_accounting BEFORE INSERT ON system_events BEGIN SELECT RAISE(ABORT,'private detail'); END`,
		`CREATE TRIGGER fail_accounting BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'private detail'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			f := newModelFixture(t)
			applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 1})
			start := callStart(t, f, "market")
			first, second := beforeCall(t, f, start), beforeCall(t, f, start)
			f.exec(t, trigger)
			fault := f.s.After(t.Context(), first, callEnd(1, 0))
			if fault == nil || strings.Contains(fault.Error(), "private detail") || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 {
				t.Fatalf("写失败没有安全回滚：%v", fault)
			}
			f.exec(t, `DROP TRIGGER fail_accounting`)
			if _, err := f.s.Before(t.Context(), start); err != fault {
				t.Fatalf("下一请求没有共享首个故障：%v", err)
			}
			if _, err := f.s.SetProviderEnabled(t.Context(), start.Provider.ID, true); err != nil {
				t.Fatal(err)
			}
			if err := f.s.After(t.Context(), second, client.CallEnd{}); err != nil {
				t.Fatalf("其他在途票据仍须完成：%v", err)
			}
			if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 1 {
				t.Fatal("在途结果丢失")
			}
			if _, err := f.s.Acquire(t.Context()); err != fault {
				t.Fatal("配置保存清掉了应用故障锁")
			}
			fresh, err := New(t.Context(), f.dependencies())
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close(context.Background())
			provider, err := fresh.Provider(t.Context(), start.Provider.ID)
			if err != nil {
				t.Fatal(err)
			}
			start.Provider = provider.Ref
			ticket, err := fresh.Before(t.Context(), start)
			if err != nil {
				t.Fatalf("重启不能继承未知票据锁：%v", err)
			}
			if err := fresh.After(t.Context(), ticket, client.CallEnd{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountingDuplicateAndInvalidResultsFailClosed(t *testing.T) {
	for _, end := range []client.CallEnd{
		{Duration: -1}, {HTTPStatus: 99}, {OK: true, HTTPStatus: 500},
		{Usage: client.Usage{Present: true, InputTokens: -1}},
		{Usage: client.Usage{Present: true, InputTokens: math.MaxInt64, OutputTokens: 1}},
		{Usage: client.Usage{Present: true, ReasoningTokens: modelPointer(int64(-1))}},
	} {
		t.Run("无效结果", func(t *testing.T) {
			f := newModelFixture(t)
			start := callStart(t, f, "market")
			if err := f.s.After(t.Context(), beforeCall(t, f, start), end); err == nil || f.s.accountingFault() == nil {
				t.Fatal("无效结果没有闭锁")
			}
			if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 {
				t.Fatal("无效结果写进用量")
			}
		})
	}
	f := newModelFixture(t)
	ticket := beforeCall(t, f, callStart(t, f, "market"))
	if err := f.s.After(t.Context(), ticket, client.CallEnd{}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.After(t.Context(), ticket, client.CallEnd{}); err == nil || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 1 {
		t.Fatal("票据重复记账")
	}
}

func TestAccountingCallbacksCannotTurnCommitIntoFailureAndCloseWaits(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 1})
	start := callStart(t, f, "market")
	ticket := beforeCall(t, f, start)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.onPublish = func(string) error { return errors.New("private-callback") }
	f.onSystem = func(ctx context.Context, id int64) error {
		close(entered)
		<-release
		if ctx.Err() != nil {
			t.Error("提交后回执被关闭取消")
		}
		return errors.New("private-callback")
	}
	done := make(chan error, 1)
	go func() { done <- f.s.After(context.Background(), ticket, callEnd(1, 0)) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("没有调用预算通知")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := f.s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("关闭丢掉记账回执：%v", err)
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil || f.s.accountingFault() != nil {
		t.Fatalf("回调失败冒充记账失败：%v", err)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 1 || !strings.Contains(logs.String(), "service.storage_failure") || strings.Contains(logs.String(), "private-callback") {
		t.Fatalf("持久事实或安全日志错误：%s", logs.String())
	}
}

func TestAccountingFailureStopsRebuiltClientsBeforeHTTP(t *testing.T) {
	f := newModelFixture(t)
	var requests atomic.Int64
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer httpServer.Close()
	input := modelInput()
	input.BaseURL = httpServer.URL
	f.create(t, input)
	newClient := func() *client.Client {
		c, err := client.New(client.Dependencies{Repository: f.s, Accounting: f.s, Slots: f.s, HTTP: httpServer.Client(), Timeout: time.Second, CompatLog: func(context.Context, client.CompatKey, client.Compat, client.Compat) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	f.exec(t, `CREATE TRIGGER fail_call BEFORE INSERT ON ai_calls BEGIN SELECT RAISE(ABORT,'private write failure'); END`)
	if _, err := newClient().Complete(t.Context(), client.Request{Kind: client.Market, User: "hello"}); err == nil {
		t.Fatal("记账失败仍返回模型成功")
	}
	f.exec(t, `DROP TRIGGER fail_call`)
	if _, err := newClient().Complete(t.Context(), client.Request{Kind: client.Market, User: "hello"}); err == nil || requests.Load() != 1 {
		t.Fatalf("新client绕过共享记账锁，HTTP=%d：%v", requests.Load(), err)
	}
}

func TestAccountingMissingUsageKeepsNullAndRealTokenPrecision(t *testing.T) {
	f := newModelFixture(t)
	start := callStart(t, f, "market")
	end := client.CallEnd{Usage: client.Usage{InputTokens: -99, OutputTokens: -99, ReasoningTokens: modelPointer(int64(-99))}}
	if err := f.s.After(t.Context(), beforeCall(t, f, start), end); err != nil {
		t.Fatal(err)
	}
	if !modelScalar[bool](t, f, `SELECT input_tokens IS NULL AND output_tokens IS NULL AND reasoning_tokens IS NULL FROM ai_calls`) {
		t.Fatal("缺usage没有保持NULL")
	}
	const externalTokens = int64(9007199254740992)
	if err := f.s.After(t.Context(), beforeCall(t, f, start), callEnd(externalTokens, 0)); err != nil {
		t.Fatal(err)
	}
	if modelScalar[int64](t, f, `SELECT input_tokens FROM ai_calls ORDER BY id DESC LIMIT 1`) != externalTokens {
		t.Fatal("外部真实token不应套界面safe整数限制")
	}
	var absent sql.NullInt64
	if err := f.db.QueryRowContext(t.Context(), `SELECT reasoning_tokens FROM ai_calls ORDER BY id DESC LIMIT 1`).Scan(&absent); err != nil || absent.Valid {
		t.Fatal("不存在的reasoning必须为NULL")
	}
}

func TestAccountingStopsCompatibilityRetryAtDailyLimit(t *testing.T) {
	f := newModelFixture(t)
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 10})
	var requests atomic.Int64
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"response_format json_schema is unsupported"},"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer httpServer.Close()
	input := modelInput()
	input.BaseURL = httpServer.URL
	f.create(t, input)
	c, err := client.New(client.Dependencies{Repository: f.s, Accounting: f.s, Slots: f.s, HTTP: httpServer.Client(), Timeout: time.Second, CompatLog: func(context.Context, client.CompatKey, client.Compat, client.Compat) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(t.Context(), client.Request{Kind: client.TitleScreen, User: "hello", Schema: &client.Schema{Name: "title", JSON: json.RawMessage(`{"type":"object"}`)}, Validate: func(string) error { return nil }})
	if !errors.Is(err, client.ErrDailyLimit) || requests.Load() != 1 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 1 {
		t.Fatalf("兼容重发绕过了逐次用量门禁：requests=%d err=%v", requests.Load(), err)
	}
}

func TestAccountingIssuedTicketFinishesAfterCloseStarts(t *testing.T) {
	f := newModelFixture(t)
	ticket := beforeCall(t, f, callStart(t, f, "market"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := f.s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("关闭没有保留已发票据的收尾：%v", err)
	}
	if err := f.s.After(context.Background(), ticket, client.CallEnd{ErrorKind: "canceled"}); err != nil {
		t.Fatalf("关闭期间丢弃已发请求的记账：%v", err)
	}
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 1 || len(f.s.accounting.tickets) != 0 {
		t.Fatal("关闭收尾没有消费票据或写入调用")
	}
}
