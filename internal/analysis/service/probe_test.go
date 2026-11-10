package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
)

func probeProvider(t *testing.T, f *modelFixture, address, code string, at int64) ProviderView {
	t.Helper()
	input := modelInput()
	input.BaseURL = address
	view := f.create(t, input)
	probeError(t, f, view.ID, code, at)
	return view
}

func probeError(t *testing.T, f *modelFixture, id int64, code string, at int64) {
	t.Helper()
	raw, err := json.Marshal(locales.Message{Code: "analysis.client." + code, Args: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE ai_providers SET error_state=?,error_at=? WHERE id=?`, string(raw), at, id)
}

func TestProbeDueOnlyEligibleProvidersAndSuccessfulResponse(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(usageAt(t, "2025-01-02T00:00:00Z"))
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["max_tokens"] != float64(5) {
			t.Errorf("探测输出不是固定5：%v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	old := f.now.Load() - 1
	eligible := []ProviderView{
		probeProvider(t, f, server.URL, "balance", old),
		probeProvider(t, f, server.URL, "daily_quota", old),
	}
	for _, code := range []string{"auth", "not_found", "unsupported_image", "thinking_too_long", "unknown"} {
		probeProvider(t, f, server.URL, code, old)
	}
	probeProvider(t, f, server.URL, "balance", f.now.Load())
	disabled := probeProvider(t, f, server.URL, "balance", old)
	f.exec(t, `UPDATE ai_providers SET enabled=0 WHERE id=?`, disabled.ID)
	input := modelInput()
	input.BaseURL = server.URL
	f.create(t, input)
	if err := f.s.ProbeDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='probe' AND ok=1`) != 2 {
		t.Fatalf("不符合资格的服务商被探测，HTTP=%d", requests.Load())
	}
	for _, p := range eligible {
		view, err := f.s.GetProvider(t.Context(), p.ID)
		if err != nil || view.ErrorState != nil || view.ErrorAt != nil {
			t.Fatalf("2xx没有恢复服务商：%+v %v", view, err)
		}
	}
}

func TestProbeDueDailyFailuresPersistAcrossRestartAndRetryNextDay(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	oldErrorAt := f.now.Load() - 24*60*60*1000
	view := probeProvider(t, f, server.URL, "balance", oldErrorAt)
	for range 2 {
		if err := f.s.ProbeDue(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = New(t.Context(), f.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 1 {
		t.Fatalf("重启重复发当天探测，HTTP=%d err=%v", requests.Load(), err)
	}
	f.now.Add(24 * 60 * 60 * 1000)
	if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 2 {
		t.Fatalf("新一天未继续探测，HTTP=%d err=%v", requests.Load(), err)
	}
	got, err := f.s.GetProvider(t.Context(), view.ID)
	if err != nil || got.ErrorState == nil || got.ErrorState.Code != "analysis.client.balance" || got.ErrorAt == nil || *got.ErrorAt != oldErrorAt {
		t.Fatalf("普通失败改掉了旧错误：%+v %v", got, err)
	}
}

func TestProbeDueManualErrorsReplaceStateAndStopAutomaticProbes(t *testing.T) {
	for _, test := range []struct {
		name, next string
		status     int
	}{
		{"无效密钥401", "auth", http.StatusUnauthorized},
		{"无效密钥403", "auth", http.StatusForbidden},
		{"模型不存在404", "not_found", http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newModelFixture(t)
			f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			view := probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
			if err := f.s.ProbeDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err := f.s.GetProvider(t.Context(), view.ID)
			if err != nil || got.ErrorState == nil || got.ErrorState.Code != "analysis.client."+test.next || got.ErrorAt == nil || *got.ErrorAt != f.now.Load() {
				t.Fatalf("需要用户处理的新错误没有替换旧状态：%+v %v", got, err)
			}
			for range 2 {
				if err := f.s.ProbeDue(t.Context()); err != nil {
					t.Fatal(err)
				}
				f.now.Add(24 * 60 * 60 * 1000)
			}
			if requests.Load() != 1 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) != 1 {
				t.Fatalf("需要用户处理的错误仍被自动探测，HTTP=%d", requests.Load())
			}
		})
	}
}

func TestProbeDueQuotaErrorsContinueOnFollowingDay(t *testing.T) {
	for _, test := range []struct {
		code, detail string
		status       int
	}{
		{"balance", "insufficient balance", http.StatusPaymentRequired},
		{"daily_quota", "quota exceeded today", http.StatusTooManyRequests},
	} {
		t.Run(test.code, func(t *testing.T) {
			f := newModelFixture(t)
			f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.detail)
			}))
			defer server.Close()
			view := probeProvider(t, f, server.URL, test.code, f.now.Load()-24*60*60*1000)
			for range 2 {
				if err := f.s.ProbeDue(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			got, err := f.s.GetProvider(t.Context(), view.ID)
			if err != nil || got.ErrorState == nil || got.ErrorState.Code != "analysis.client."+test.code || requests.Load() != 1 {
				t.Fatalf("余额或额度错误未保留或当天重复探测：%+v HTTP=%d %v", got, requests.Load(), err)
			}
			f.now.Add(24 * 60 * 60 * 1000)
			if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 2 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) != 2 {
				t.Fatalf("余额或额度错误次日未再探测，HTTP=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestProbeDueDailyBudgetDoesNotConsumeAttempt(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, modelResponse)
	}))
	defer server.Close()
	probeProvider(t, f, server.URL, "daily_quota", f.now.Load()-24*60*60*1000)
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 1})
	usageCall(t, f, f.now.Load()-1, nil, "test", true, 1, 1, 0, nil)
	if err := f.s.ProbeDue(t.Context()); !errors.Is(err, client.ErrDailyLimit) {
		t.Fatalf("探测没有被日额度挡住：%v", err)
	}
	if requests.Load() != 0 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) != 0 {
		t.Fatal("未发出的探测消费了当天机会")
	}
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 10})
	if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 1 {
		t.Fatalf("调高日额后不能重试，HTTP=%d err=%v", requests.Load(), err)
	}
}

func TestProbeDueCoalescesConcurrentCalls(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer func() {
		once.Do(func() { close(release) })
		server.Close()
	}()
	probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
	done := make(chan error, 1)
	go func() { done <- f.s.ProbeDue(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("探测没有到达假服务端")
	}
	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() {
			if err := f.s.ProbeDue(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	callers.Wait()
	once.Do(func() { close(release) })
	if err := <-done; err != nil || requests.Load() != 1 {
		t.Fatalf("并发调用重复探测，HTTP=%d err=%v", requests.Load(), err)
	}
}

func TestProbeDueRechecksChangesWhileWaitingForSlot(t *testing.T) {
	for _, change := range []string{"保存", "停用", "删除", "清错", "改为认证错误"} {
		t.Run(change, func(t *testing.T) {
			f := newModelFixture(t)
			f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
			applyModelSettings(t, f, map[string]any{"ai.max_concurrent_requests": 1})
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
			}))
			defer server.Close()
			view := probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
			release := acquireSlot(t, f.s)
			defer release()
			done := make(chan error, 1)
			go func() { done <- f.s.ProbeDue(t.Context()) }()
			waitModelJobs(t, f.s, 3)
			switch change {
			case "保存":
				input := modelInput()
				input.BaseURL = server.URL
				if _, err := f.s.UpdateProvider(t.Context(), view.ID, input); err != nil {
					t.Fatal(err)
				}
			case "停用":
				if _, err := f.s.SetProviderEnabled(t.Context(), view.ID, false); err != nil {
					t.Fatal(err)
				}
			case "删除":
				if err := f.s.DeleteProvider(t.Context(), view.ID); err != nil {
					t.Fatal(err)
				}
			case "清错":
				f.exec(t, `UPDATE ai_providers SET error_state=NULL,error_at=NULL WHERE id=?`, view.ID)
			case "改为认证错误":
				probeError(t, f, view.ID, "auth", f.now.Load()-24*60*60*1000)
			}
			release()
			if err := <-done; err != nil || requests.Load() != 0 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 {
				t.Fatalf("等待期间配置变化后仍发请求，HTTP=%d err=%v", requests.Load(), err)
			}
		})
	}
}

type probeCompatGate struct {
	*Service
	entered chan struct{}
	release <-chan struct{}
}

func (r probeCompatGate) LoadCompat(ctx context.Context, key client.CompatKey) (client.Compat, error) {
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		return client.Compat{}, ctx.Err()
	}
	return r.Service.LoadCompat(ctx, key)
}

func TestProbeFirstBeforeRechecksAfterProviderWasRead(t *testing.T) {
	for _, change := range []string{"清错", "改为认证错误", "当天已有探测"} {
		t.Run(change, func(t *testing.T) {
			f := newModelFixture(t)
			f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
			}))
			defer server.Close()
			view := probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
			p, err := f.s.Provider(t.Context(), view.ID)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			c, err := client.New(client.Dependencies{Repository: probeCompatGate{f.s, entered, release}, Accounting: f.s, Slots: f.s, HTTP: server.Client(), Timeout: time.Second, CompatLog: func(context.Context, client.CompatKey, client.Compat, client.Compat) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(t.Context(), probeContextKey{}, &probeGuard{provider: p.Ref})
			done := make(chan error, 1)
			go func() { _, err := c.Probe(ctx, p.Ref.ID); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("探测未进入首次记账前的等待")
			}
			switch change {
			case "清错":
				if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "test_success"}); err != nil {
					t.Fatal(err)
				}
			case "改为认证错误":
				if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}}); err != nil {
					t.Fatal(err)
				}
			case "当天已有探测":
				f.exec(t, `INSERT INTO ai_calls(at,provider_id,purpose) VALUES(?,?,'probe')`, f.now.Load(), view.ID)
			}
			once.Do(func() { close(release) })
			if err := <-done; !errors.Is(err, client.ErrConfigChanged) || requests.Load() != 0 {
				t.Fatalf("首次Before没有重核资格，HTTP=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestProbeDueUsesTrueMidnightAcrossDST(t *testing.T) {
	for _, day := range []struct{ start, end string }{
		{"2025-03-09T05:00:00Z", "2025-03-10T04:00:00Z"},
		{"2025-11-02T04:00:00Z", "2025-11-03T05:00:00Z"},
	} {
		t.Run(day.start, func(t *testing.T) {
			f := newModelFixture(t)
			f.now.Store(usageAt(t, day.start))
			applyModelSettings(t, f, map[string]any{"general.timezone": "America/New_York"})
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			probeProvider(t, f, server.URL, "daily_quota", f.now.Load()-1)
			if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 1 {
				t.Fatalf("当地零点没有放行探测：%v", err)
			}
			f.now.Store(usageAt(t, day.end) - 1)
			if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 1 {
				t.Fatalf("当地次日零点之前重复探测：%v", err)
			}
			f.now.Add(1)
			if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 2 {
				t.Fatalf("当地次日零点未恢复探测：%v", err)
			}
		})
	}
}

func TestProbeDueTimezoneChangesAndRollbackKeepProtectedDay(t *testing.T) {
	for _, change := range []struct{ timezone, boundary string }{
		{"Asia/Shanghai", "2025-01-03T00:00:00Z"},
		{"America/Los_Angeles", "2025-01-03T08:00:00Z"},
	} {
		t.Run(change.timezone, func(t *testing.T) {
			f := newModelFixture(t)
			f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
			if err := f.s.ProbeDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			applyModelSettings(t, f, map[string]any{"general.timezone": change.timezone})
			f.now.Add(-60 * 60 * 1000)
			if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 1 {
				t.Fatalf("回拨重复探测：%v", err)
			}
			f.now.Store(usageAt(t, change.boundary) - 1)
			if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 1 {
				t.Fatalf("改时区提前恢复探测：%v", err)
			}
			f.now.Add(1)
			if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 2 {
				t.Fatalf("保护窗结束后没有恢复探测：%v", err)
			}
		})
	}
}

func TestProbeDueRejectsCorruptStateBeforeHTTP(t *testing.T) {
	for _, damage := range []string{"缺错误时间", "零错误时间", "未来错误时间", "坏错误JSON", "坏配置版本", "缺时钟", "空探测时间", "未来探测时间"} {
		t.Run(damage, func(t *testing.T) {
			f := newModelFixture(t)
			f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
			defer server.Close()
			view := probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
			switch damage {
			case "缺错误时间":
				f.exec(t, `UPDATE ai_providers SET error_at=NULL WHERE id=?`, view.ID)
			case "零错误时间":
				f.exec(t, `UPDATE ai_providers SET error_at=0 WHERE id=?`, view.ID)
			case "未来错误时间":
				f.exec(t, `UPDATE ai_providers SET error_at=? WHERE id=?`, f.now.Load()+1, view.ID)
			case "坏错误JSON":
				f.exec(t, `UPDATE ai_providers SET error_state='{}' WHERE id=?`, view.ID)
			case "坏配置版本":
				f.exec(t, `UPDATE meta SET value='bad' WHERE key=?`, providerTokenKey(view.ID))
			case "缺时钟":
				f.exec(t, `DELETE FROM meta WHERE key='quota.clock'`)
			case "空探测时间":
				f.exec(t, `INSERT INTO ai_calls(provider_id,purpose) VALUES(?,'probe')`, view.ID)
			case "未来探测时间":
				f.exec(t, `INSERT INTO ai_calls(provider_id,purpose,at) VALUES(?,'probe',?)`, view.ID, f.now.Load()+1)
			}
			if err := f.s.ProbeDue(t.Context()); err == nil || requests.Load() != 0 {
				t.Fatalf("损坏数据没有闭锁，HTTP=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestProbeDueNetworkFailureConsumesOneAttempt(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	oldErrorAt := f.now.Load() - 24*60*60*1000
	view := probeProvider(t, f, server.URL, "balance", oldErrorAt)
	for range 2 {
		if err := f.s.ProbeDue(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 1 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='probe' AND ok=0`) != 1 {
		t.Fatal("网络失败没有作为当天的一次真实探测")
	}
	got, err := f.s.GetProvider(t.Context(), view.ID)
	if err != nil || got.ErrorState == nil || got.ErrorState.Code != "analysis.client.balance" || got.ErrorAt == nil || *got.ErrorAt != oldErrorAt {
		t.Fatalf("网络失败改掉了旧错误：%+v %v", got, err)
	}
	f.now.Add(24 * 60 * 60 * 1000)
	if err := f.s.ProbeDue(t.Context()); err != nil || requests.Load() != 2 {
		t.Fatalf("网络失败次日未再探测，HTTP=%d err=%v", requests.Load(), err)
	}
}

func TestProbeDueCompatibilityRetryKeepsFiveAndDailyGate(t *testing.T) {
	for _, test := range []struct {
		name  string
		limit int64
	}{{"无限制", 0}, {"额度用满", 2}} {
		t.Run(test.name, func(t *testing.T) {
			limit := test.limit
			f := newModelFixture(t)
			f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
			applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": limit})
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				key := "max_tokens"
				if n > 1 {
					key = "max_completion_tokens"
				}
				if body[key] != float64(5) {
					t.Errorf("探测套用了普通输出floor：%v", body)
				}
				if n == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"max_tokens is unsupported; use max_completion_tokens"},"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"length","message":{"content":""}}]}`)
			}))
			defer server.Close()
			view := probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
			f.exec(t, `INSERT INTO ai_compat(provider_id,model,json_mode,send_temperature,max_tokens_param,max_tokens_floor,updated_at) VALUES(?,'vision-model','schema',1,'max_tokens',8000,?)`, view.ID, f.now.Load())
			err := f.s.ProbeDue(t.Context())
			wantRequests := int64(2)
			if limit > 0 {
				wantRequests = 1
				if !errors.Is(err, client.ErrDailyLimit) {
					t.Fatalf("兼容重发绕过日额：%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != wantRequests || modelScalar[int64](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='probe'`) != wantRequests || modelScalar[int64](t, f, `SELECT max_tokens_floor FROM ai_compat WHERE provider_id=?`, view.ID) != 8000 {
				t.Fatalf("兼容重发、记账或floor不符，HTTP=%d", requests.Load())
			}
		})
	}
}

func TestProbeDueAfterFaultStopsAllProvidersAndFutureCalls(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, modelResponse)
	}))
	defer server.Close()
	for range 2 {
		probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
	}
	f.exec(t, `CREATE TRIGGER fail_probe BEFORE INSERT ON ai_calls BEGIN SELECT RAISE(FAIL,'probe after failure'); END`)
	if err := f.s.ProbeDue(t.Context()); !errors.Is(err, client.ErrInfrastructure) || requests.Load() != 1 {
		t.Fatalf("记账失败没有停止后续探测：HTTP=%d err=%v", requests.Load(), err)
	}
	f.exec(t, `DROP TRIGGER fail_probe`)
	if err := f.s.ProbeDue(t.Context()); err == nil || requests.Load() != 1 {
		t.Fatalf("重入绕过了记账闭锁：HTTP=%d err=%v", requests.Load(), err)
	}
}

func TestProbeDueCloseCancelsAndWaitsForAccounting(t *testing.T) {
	f := newModelFixture(t)
	f.now.Store(usageAt(t, "2025-01-02T12:00:00Z"))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		once.Do(func() { close(release) })
		server.Close()
	}()
	probeProvider(t, f, server.URL, "balance", f.now.Load()-24*60*60*1000)
	done := make(chan error, 1)
	go func() { done <- f.s.ProbeDue(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("探测没有进入HTTP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("关闭未取消探测：%v", err)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='probe' AND ok=0`) != 1 {
		t.Fatal("关闭没有等已发请求记账收尾")
	}
	if err := f.s.ProbeDue(t.Context()); err == nil {
		t.Fatal("关闭后仍接探测")
	}
}
