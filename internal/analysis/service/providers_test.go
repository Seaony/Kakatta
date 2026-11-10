package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func TestProviderEncryptionSafeViewsAndKeyReplacement(t *testing.T) {
	f := newModelFixture(t)
	input := modelInput()
	input.APIKey = modelPointer("private-key-测试")
	input.TextModel, input.Preset = modelPointer("text-model"), modelPointer("custom")
	view := f.create(t, input)
	if !view.APIKeySet || view.TextModel == nil || *view.TextModel != "text-model" {
		t.Fatalf("安全视图丢配置：%+v", view)
	}
	uuid := modelScalar[string](t, f, `SELECT uuid FROM ai_providers WHERE id=?`, view.ID)
	encrypted := modelScalar[[]byte](t, f, `SELECT api_key_enc FROM ai_providers WHERE id=?`, view.ID)
	if len(uuid) != 36 || strings.Contains(string(encrypted), *input.APIKey) {
		t.Fatal("密钥未按独立UUID加密")
	}
	plain, err := f.cipher.Open(encrypted, "ai_providers", "api_key_enc", uuid)
	if err != nil || string(plain) != *input.APIKey {
		t.Fatalf("正确AAD无法读取：%v", err)
	}
	if _, err := f.cipher.Open(encrypted, "ai_providers", "api_key_enc", "wrong-row"); err == nil {
		t.Fatal("错误行AAD不应读到密钥")
	}
	request, err := f.s.Provider(t.Context(), view.ID)
	if err != nil || request.APIKey != *input.APIKey || request.Ref.ConfigToken == "" {
		t.Fatal("请求配置没有解密密钥或版本")
	}
	listed, err := f.s.ListProviders(t.Context())
	if err != nil || len(listed) != 1 {
		t.Fatal(err)
	}
	data, _ := json.Marshal(listed)
	for _, secret := range []string{*input.APIKey, request.Ref.ConfigToken, uuid, "api_key_enc", "config_token"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("安全视图泄漏内部信息：%s", data)
		}
	}
	input.APIKey = nil
	if _, err := f.s.UpdateProvider(t.Context(), view.ID, input); err != nil {
		t.Fatal(err)
	}
	if got := modelScalar[[]byte](t, f, `SELECT api_key_enc FROM ai_providers WHERE id=?`, view.ID); !reflect.DeepEqual(got, encrypted) {
		t.Fatal("省略密钥不应重新加密或清空")
	}
	for _, key := range []string{"new-whole-key", ""} {
		input.APIKey = &key
		saved, err := f.s.UpdateProvider(t.Context(), view.ID, input)
		if err != nil || saved.Provider.APIKeySet != (key != "") {
			t.Fatalf("显式替换/清空失败：%+v %v", saved, err)
		}
		got, err := f.s.Provider(t.Context(), view.ID)
		if err != nil || got.APIKey != key {
			t.Fatalf("请求使用了错误密钥：%+v %v", got, err)
		}
	}
	if got := modelScalar[string](t, f, `SELECT uuid FROM ai_providers WHERE id=?`, view.ID); got != uuid {
		t.Fatal("保存不应改变加密AAD身份")
	}
	if !modelScalar[bool](t, f, `SELECT api_key_enc IS NULL FROM ai_providers WHERE id=?`, view.ID) {
		t.Fatal("明确清空应存NULL")
	}
}

func TestProviderValidationAndAddressNormalization(t *testing.T) {
	f := newModelFixture(t)
	input := modelInput()
	input.BaseURL = "http://127.0.0.1:11434/prefix/v1/chat/completions///"
	result, err := f.s.CreateProvider(t.Context(), input)
	if err != nil || result.Provider.BaseURL != "http://127.0.0.1:11434/prefix/v1" || len(result.Warnings) != 1 || result.Warnings[0].Code != "ai.url_normalized" {
		t.Fatalf("应保留前缀并指出完整接口后缀已去掉：%+v %v", result, err)
	}
	for _, tc := range []struct {
		field  string
		change func(*ProviderInput)
	}{
		{"name", func(i *ProviderInput) { i.Name = " \t" }},
		{"vision_model", func(i *ProviderInput) { i.VisionModel = "" }},
		{"preset", func(i *ProviderInput) { i.Preset = modelPointer("missing") }},
		{"api_key", func(i *ProviderInput) { i.APIKey = modelPointer("secret\r\nInjected: yes") }},
		{"base_url", func(i *ProviderInput) { i.BaseURL = "https://user:secret@invalid.local/v1" }},
		{"base_url", func(i *ProviderInput) { i.BaseURL = "https://invalid.local/v1?" }},
		{"base_url", func(i *ProviderInput) { i.BaseURL = "https://invalid.local/v1#" }},
		{"base_url", func(i *ProviderInput) { i.BaseURL = "file:///secret" }},
	} {
		input := modelInput()
		tc.change(&input)
		_, err := f.s.CreateProvider(t.Context(), input)
		var invalid *service.ServiceError
		if !errors.As(err, &invalid) || invalid.Kind != service.ErrorInvalid || invalid.Reason.Args["field"] != tc.field || strings.Contains(err.Error(), "secret") {
			t.Fatalf("字段失败应为安全结构原因：%s %v", tc.field, err)
		}
	}
	if count := modelScalar[int](t, f, `SELECT count(*) FROM ai_providers`); count != 1 {
		t.Fatal("校验失败仍写入了配置")
	}
	duplicate := modelInput()
	duplicate.TextModel = modelPointer("   ")
	if view := f.create(t, duplicate); view.TextModel != nil {
		t.Fatal("空文字模型应规范成跟随图片模型")
	}
}

func TestProviderSaveRotatesTokenAndResetsOnlyChangedAddressOrModels(t *testing.T) {
	for _, field := range []string{"same", "key", "base_url", "vision_model", "text_model"} {
		t.Run(field, func(t *testing.T) {
			f := newModelFixture(t)
			input := modelInput()
			input.TextModel = modelPointer("text-model")
			view := f.create(t, input)
			previous, _ := f.s.Provider(t.Context(), view.ID)
			key := client.CompatKey{Provider: previous.Ref, BaseURL: previous.BaseURL, Model: previous.VisionModel}
			if _, err := f.s.SaveCompat(t.Context(), key, client.Compat{JSONMode: client.JSONText, MaxTokensParam: "max_completion_tokens"}); err != nil {
				t.Fatal(err)
			}
			if err := f.s.ChangeProvider(t.Context(), previous.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.balance"}}); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "key":
				input.APIKey = modelPointer("replacement")
			case "base_url":
				input.BaseURL += "/other"
			case "vision_model":
				input.VisionModel += "-new"
			case "text_model":
				input.TextModel = modelPointer("text-new")
			}
			if _, err := f.s.UpdateProvider(t.Context(), view.ID, input); err != nil {
				t.Fatal(err)
			}
			current, err := f.s.Provider(t.Context(), view.ID)
			if err != nil || current.Ref.ConfigToken == previous.Ref.ConfigToken || current.ErrorState != nil {
				t.Fatalf("保存应换代并清错误：%+v %v", current, err)
			}
			want := 0
			if field == "same" || field == "key" {
				want = 1
			}
			if count := modelScalar[int](t, f, `SELECT count(*) FROM ai_compat`); count != want {
				t.Fatalf("兼容清理范围错误：%d，应为%d", count, want)
			}
			if count := modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE resolved_at IS NULL`); count != 0 {
				t.Fatal("保存没有解决旧事件")
			}
			for _, operation := range []func() error{
				func() error { _, e := f.s.LoadCompat(t.Context(), key); return e },
				func() error { _, e := f.s.SaveCompat(t.Context(), key, client.DefaultCompat()); return e },
				func() error {
					return f.s.ChangeProvider(t.Context(), previous.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}})
				},
			} {
				if err := operation(); err != client.ErrConfigChanged {
					t.Fatalf("旧配置仍能覆盖新保存：%v", err)
				}
			}
		})
	}
}

func TestProviderReorderKeepsInFlightConfigurationAndEnableResets(t *testing.T) {
	f := newModelFixture(t)
	first, second := f.create(t, modelInput()), f.create(t, modelInput())
	old, _ := f.s.Provider(t.Context(), first.ID)
	key := client.CompatKey{Provider: old.Ref, BaseURL: old.BaseURL, Model: old.VisionModel}
	_, err := f.s.SaveCompat(t.Context(), key, client.Compat{JSONMode: client.JSONText, MaxTokensParam: "max_tokens"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ChangeProvider(t.Context(), old.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}}); err != nil {
		t.Fatal(err)
	}
	views, err := f.s.ReorderProviders(t.Context(), []int64{second.ID, first.ID})
	if err != nil || len(views) != 2 || views[0].ID != second.ID || views[1].SortIndex != 1 {
		t.Fatalf("完整排序失败：%+v %v", views, err)
	}
	current, _ := f.s.Provider(t.Context(), first.ID)
	compat, err := f.s.LoadCompat(t.Context(), key)
	if err != nil || current.Ref != old.Ref || current.ErrorState == nil || compat.JSONMode != client.JSONText {
		t.Fatal("排序误改配置代次、错误或兼容")
	}
	for _, ids := range [][]int64{{}, {first.ID}, {first.ID, first.ID}, {first.ID, 999}, {first.ID, second.ID, 999}} {
		if _, err := f.s.ReorderProviders(t.Context(), ids); err == nil {
			t.Fatalf("非法完整集合得到接纳：%v", ids)
		}
	}
	for _, enabled := range []bool{false, true} {
		before, _ := f.s.Provider(t.Context(), first.ID)
		view, err := f.s.SetProviderEnabled(t.Context(), first.ID, enabled)
		after, _ := f.s.Provider(t.Context(), first.ID)
		if err != nil || view.Enabled != enabled || view.ErrorState != nil || before.Ref == after.Ref || modelScalar[int](t, f, `SELECT count(*) FROM ai_compat`) != 1 {
			t.Fatalf("启停没有按用户保存清错误/换代/保留兼容：%+v %v", view, err)
		}
	}
}

func TestProviderDeleteKeepsUsageAndEventHistoryAndNeverReusesID(t *testing.T) {
	f := newModelFixture(t)
	view := f.create(t, modelInput())
	p, _ := f.s.Provider(t.Context(), view.ID)
	key := client.CompatKey{Provider: p.Ref, BaseURL: p.BaseURL, Model: p.VisionModel}
	if _, err := f.s.SaveCompat(t.Context(), key, client.DefaultCompat()); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO ai_calls(provider_id,purpose,input_tokens,output_tokens) VALUES(?,'test',7,8)`, view.ID)
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "blocked", Reason: &locales.Message{Code: "analysis.client.auth"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteProvider(t.Context(), view.ID); err != nil {
		t.Fatal(err)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE provider_id=?`, view.ID) != 1 || modelScalar[int](t, f, `SELECT count(*) FROM system_events WHERE resolved_at IS NOT NULL`) != 1 || modelScalar[int](t, f, `SELECT count(*) FROM ai_compat`) != 0 || modelScalar[int](t, f, `SELECT count(*) FROM meta WHERE key=?`, providerTokenKey(view.ID)) != 0 {
		t.Fatal("删除丢掉历史或没有清从属兼容/版本")
	}
	if next := f.create(t, modelInput()); next.ID <= view.ID {
		t.Fatal("删除后复用了历史编号")
	}
	if err := f.s.ChangeProvider(t.Context(), p.Ref, client.ProviderChange{Kind: "test_success"}); err != client.ErrConfigChanged {
		t.Fatal("删除后的旧回包仍能写入")
	}
	_, err := f.s.GetProvider(context.Background(), view.ID)
	var missing *service.ServiceError
	if !errors.As(err, &missing) || missing.Kind != service.ErrorNotFound {
		t.Fatalf("删除对象查询应为not_found：%v", err)
	}
}

func TestProviderReorderFailureRollsBackTheWholeOrder(t *testing.T) {
	f := newModelFixture(t)
	first, second := f.create(t, modelInput()), f.create(t, modelInput())
	before, _ := f.s.ListProviders(t.Context())
	published := len(f.published)
	f.exec(t, `CREATE TRIGGER fail_second_sort BEFORE UPDATE OF sort_index ON ai_providers WHEN NEW.id=`+fmt.Sprint(first.ID)+` BEGIN SELECT RAISE(ABORT,'sort failure'); END`)
	if _, err := f.s.ReorderProviders(t.Context(), []int64{second.ID, first.ID}); err == nil {
		t.Fatal("排序中途失败未传出")
	}
	after, err := f.s.ListProviders(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) || len(f.published) != published {
		t.Fatal("排序失败留下了半份新顺序或发布成功变化")
	}
}

func TestProviderConfigTokenPreventsChangeAwayAndBack(t *testing.T) {
	f := newModelFixture(t)
	input := modelInput()
	view := f.create(t, input)
	old, _ := f.s.Provider(t.Context(), view.ID)
	input.APIKey = modelPointer("changed")
	if _, err := f.s.UpdateProvider(t.Context(), view.ID, input); err != nil {
		t.Fatal(err)
	}
	input.APIKey = modelPointer("")
	if _, err := f.s.UpdateProvider(t.Context(), view.ID, input); err != nil {
		t.Fatal(err)
	}
	back, err := f.s.Provider(t.Context(), view.ID)
	if err != nil || back.APIKey != old.APIKey || back.Ref == old.Ref {
		t.Fatal("改回相同值后复用了旧代次")
	}
	if err := f.s.ChangeProvider(t.Context(), old.Ref, client.ProviderChange{Kind: "test_success"}); err != client.ErrConfigChanged {
		t.Fatal("旧的成功回包仍能改变新配置")
	}
}
