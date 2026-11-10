package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func TestRepositoryRecordsProviderAndSystemEventBeforeNotification(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	f.onSystem = func(ctx context.Context, id int64) error {
		var state, message string
		if err := f.db.QueryRowContext(ctx, `SELECT p.error_state,e.message FROM ai_providers p JOIN system_events e ON e.subject_id=p.id AND e.kind='ai_error' WHERE e.id=?`, id).Scan(&state, &message); err != nil {
			return err
		}
		var event locales.Message
		if err := json.Unmarshal([]byte(message), &event); err != nil {
			return err
		}
		if !strings.Contains(state, "analysis.client.balance") || event.Code != "ai.provider_error" || event.Args["provider"] != view.Name || event.Args["reason"].(map[string]any)["code"] != "analysis.client.balance" {
			t.Errorf("通知前状态/嵌套安全原因没有同事务保存：%s %s", state, message)
		}
		return nil
	}
	reason := locales.Message{Code: "analysis.client.balance", Args: map[string]any{"status": 402, "detail": "[REDACTED]"}}
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &reason}); err != nil {
		t.Fatal(err)
	}
	reason.Args["detail"] = "调用方后来改动"
	f.now.Add(100)
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth", Args: map[string]any{"status": 401, "detail": "new safe reason"}}}); err != nil {
		t.Fatal(err)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM system_events`) != 1 || len(f.notified) != 1 {
		t.Fatal("同一未解决问题被重新创建或重复通知")
	}
	if raw := modelScalar[string](t, f, `SELECT message FROM system_events`); !strings.Contains(raw, "analysis.client.auth") || strings.Contains(raw, "调用方后来改动") {
		t.Fatal("事件没有更新最新安全原因或共享了调用方数据")
	}
	if !slices.Contains(f.published, "ai.providers.changed") || !slices.Contains(f.published, "system_event.changed") {
		t.Fatal("提交后没有发布真实资源变化")
	}
}

func TestRepositorySuccessSemanticsAndNewErrorAfterResolution(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	block := func() {
		t.Helper()
		if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.daily_quota"}}); err != nil {
			t.Fatal(err)
		}
	}
	block()
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "request_success"}); err != nil {
		t.Fatal(err)
	}
	current, _ := f.s.Provider(t.Context(), view.ID)
	if current.ErrorState == nil || modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE resolved_at IS NULL`) != 0 {
		t.Fatal("普通成功只解决事件，不清错误状态")
	}
	for _, kind := range []string{"test_success", "probe_success"} {
		block()
		if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: kind}); err != nil {
			t.Fatal(err)
		}
		current, _ := f.s.Provider(t.Context(), view.ID)
		if current.ErrorState != nil || current.Ref != p.Ref || modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE resolved_at IS NULL`) != 0 {
			t.Fatalf("完整试分析/探测没有清错误，或偷偷换代：%s %+v", kind, current)
		}
	}
	if len(f.notified) != 3 {
		t.Fatal("旧问题解决后新发生的问题应独立通知")
	}
}

func TestRepositoryWriteFailureRollsBackStateAndNeverPublishes(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_event BEFORE INSERT ON system_events BEGIN SELECT RAISE(ABORT,'private SQL detail'); END`,
		`CREATE TRIGGER fail_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'private SQL detail'); END`,
	} {
		f := newModelFixture(t)
		view := f.create(t, modelInput())
		p, _ := f.s.Provider(t.Context(), view.ID)
		before := len(f.published)
		f.exec(t, trigger)
		err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}})
		var unavailable *service.ServiceError
		if !errors.As(err, &unavailable) || unavailable.Kind != service.ErrorUnavailable || strings.Contains(err.Error(), "private") {
			t.Fatalf("存储错误没有关闭写路径或泄漏原文：%v", err)
		}
		current, e := f.s.Provider(t.Context(), view.ID)
		if e != nil || current.ErrorState != nil || modelScalar[int](t, f, `SELECT count(*) FROM system_events`) != 0 || len(f.notified) != 0 || len(f.published) != before {
			t.Fatal("失败事务留下状态、事件或发布了成功变化")
		}
	}
}

func TestProviderSaveFailurePreservesKeyTokenCompatAndError(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_token BEFORE UPDATE ON meta WHEN NEW.key LIKE 'provider.config.%' BEGIN SELECT RAISE(ABORT,'secret-write'); END`,
		`CREATE TRIGGER fail_resolve BEFORE UPDATE ON system_events BEGIN SELECT RAISE(ABORT,'secret-write'); END`,
		`CREATE TRIGGER fail_compat BEFORE DELETE ON ai_compat BEGIN SELECT RAISE(ABORT,'secret-write'); END`,
	} {
		f := newModelFixture(t)
		input := modelInput()
		input.APIKey = modelPointer("original-secret")
		view := f.create(t, input)
		p, _ := f.s.Provider(t.Context(), view.ID)
		key := client.CompatKey{Provider: p.Ref, BaseURL: p.BaseURL, Model: p.VisionModel}
		if _, err := f.s.SaveCompat(t.Context(), key, client.DefaultCompat()); err != nil {
			t.Fatal(err)
		}
		if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}}); err != nil {
			t.Fatal(err)
		}
		before := len(f.published)
		f.exec(t, trigger)
		input.APIKey, input.BaseURL = modelPointer("replacement-secret"), p.BaseURL+"/new"
		if _, err := f.s.UpdateProvider(t.Context(), view.ID, input); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("保存失败未外抛安全错误：%v", err)
		}
		current, err := f.s.Provider(t.Context(), view.ID)
		if err != nil || current.Ref != p.Ref || current.APIKey != "original-secret" || current.BaseURL != p.BaseURL || current.ErrorState == nil || modelScalar[int](t, f, `SELECT count(*) FROM ai_compat`) != 1 || modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE resolved_at IS NULL`) != 1 || len(f.published) != before {
			t.Fatal("保存失败未原子保留原配置、事件和兼容")
		}
	}
}

func TestRepositoryReadsNeverPersistClockAndReturnsIndependentState(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth", Args: map[string]any{"detail": "safe"}}}); err != nil {
		t.Fatal(err)
	}
	before := modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey)
	f.now.Add(int64(48 * time.Hour / time.Millisecond))
	views, err := f.s.ListProviders(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	views[0].ErrorState.Args["detail"] = "changed"
	if _, err := f.s.GetProvider(t.Context(), view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Providers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.LoadCompat(t.Context(), client.CompatKey{Provider: p.Ref, BaseURL: p.BaseURL, Model: p.VisionModel}); err != nil {
		t.Fatal(err)
	}
	after := modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey)
	if after != before || strings.Contains(modelScalar[string](t, f, `SELECT error_state FROM ai_providers WHERE id=?`, view.ID), "changed") {
		t.Fatal("只读查询写了时钟或返回了共享状态")
	}
}

func TestRepositoryCipherCorruptionAndMissingMetadataFailClosed(t *testing.T) {
	f := newModelFixture(t)
	input := modelInput()
	input.APIKey = modelPointer("a-private-key")
	first := f.create(t, input)
	input.APIKey = modelPointer("b-private-key")
	second := f.create(t, input)
	f.exec(t, `UPDATE ai_providers SET api_key_enc=(SELECT api_key_enc FROM ai_providers WHERE id=?) WHERE id=?`, second.ID, first.ID)
	if result, err := f.s.ListProviders(t.Context()); err == nil || result != nil || strings.Contains(err.Error(), "private-key") {
		t.Fatalf("AAD错行不能降成无密钥或部分列表：%+v %v", result, err)
	}
	if result, err := f.s.Providers(t.Context()); err == nil || result != nil {
		t.Fatal("坏配置被静默跳过")
	}
	f.exec(t, `DELETE FROM meta WHERE key=?`, providerTokenKey(second.ID))
	if _, err := f.s.Provider(t.Context(), second.ID); err == nil || err == client.ErrConfigChanged {
		t.Fatal("配置版本缺失应视为存储异常，不能生成新token或默认值")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM meta WHERE key=?`, providerTokenKey(second.ID)) != 0 {
		t.Fatal("只读配置查询偷偷修复了元数据")
	}
}

func TestProviderConfigTokenInitializationAndRestartPersistence(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	old, _ := f.s.Provider(t.Context(), view.ID)
	if err := f.s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO ai_providers(uuid,name,base_url,vision_model,sort_index) VALUES('legacy-row','已有配置','http://127.0.0.1:11434/v1','legacy-model',99)`)
	restarted, err := New(t.Context(), f.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	f.s = restarted
	current, _ := f.s.Provider(t.Context(), view.ID)
	all, err := f.s.Providers(t.Context())
	if err != nil || current.Ref != old.Ref || len(all) != 2 || !validToken(all[1].Ref.ConfigToken) {
		t.Fatalf("重启应保现有代次，只初始化真正旧行：%+v %v", all, err)
	}
	f.exec(t, `UPDATE meta SET value='damaged' WHERE key=?`, providerTokenKey(view.ID))
	if bad, err := New(t.Context(), f.dependencies()); bad != nil || err == nil {
		t.Fatal("损坏代次不能被构造器静默换新")
	}
}

func TestProviderErrorTimestampUsesProtectedClock(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	protected := f.now.Load()
	f.now.Add(-int64(24 * time.Hour / time.Millisecond))
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}}); err != nil {
		t.Fatal(err)
	}
	if at := modelScalar[int64](t, f, `SELECT error_at FROM ai_providers WHERE id=?`, view.ID); at != protected {
		t.Fatalf("系统回拨让错误时间倒退：%d，应为%d", at, protected)
	}
	if at := modelScalar[int64](t, f, `SELECT created_at FROM system_events`); at != protected {
		t.Fatal("同事务事件没有使用同一保护时间")
	}
}
