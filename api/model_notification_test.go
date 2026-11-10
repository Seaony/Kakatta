package api_test

import (
	"slices"
	"strings"
	"testing"
)

func TestModelBoundaryKeepsSecretsAndMoneySeparate(t *testing.T) {
	document := decoded(t)
	generation := schema(t, document, "GenerateProfileInput")
	taskID := object(t, object(t, generation["properties"])["task_id"])
	if slices.Contains(stringsIn(t, generation["required"]), "task_id") || !allowsNull(taskID) {
		t.Fatal("生成草稿的任务引用应保留可省略和 null")
	}
	integerTaskID := false
	for _, raw := range taskID["anyOf"].([]any) {
		branch := object(t, raw)
		if branch["type"] != "integer" {
			continue
		}
		integerTaskID = true
		if integerBound(t, branch, "minimum") != 1 || integerBound(t, branch, "maximum") != 9007199254740991 {
			t.Fatal("生成草稿的任务引用必须是安全范围内的正整数")
		}
	}
	if !integerTaskID {
		t.Fatal("生成草稿的任务引用缺少整数分支")
	}
	properties := object(t, schema(t, document, "ProviderView")["properties"])
	for _, field := range []string{"api_key", "api_key_enc", "uuid", "config_token"} {
		if _, exists := properties[field]; exists {
			t.Fatalf("服务商安全视图公开了 %s", field)
		}
	}
	input := schema(t, document, "ProviderInput")
	if slices.Contains(stringsIn(t, input["required"]), "api_key") {
		t.Fatal("整体保存仍应允许省略密钥以保留原值")
	}
	key := object(t, object(t, input["properties"])["api_key"])
	if !allowsNull(key) || key["writeOnly"] != true || key["x-sensitive"] != true {
		t.Fatal("密钥没有保留明确的只写及 null 语义")
	}
	draft := object(t, schema(t, document, "GeneratedDraft")["properties"])
	for _, field := range []string{"request", "calls", "provider", "api_key"} {
		if _, exists := draft[field]; exists {
			t.Fatalf("草稿公开了内部请求字段 %s", field)
		}
	}
	suggestions := object(t, schema(t, document, "SuggestionsView")["properties"])
	for _, field := range []string{"budget_min", "budget_max"} {
		value := object(t, suggestions[field])
		if !allowsNull(value) {
			t.Fatalf("建议金额 %s 不能表达未知", field)
		}
		integer := false
		for _, branch := range value["anyOf"].([]any) {
			if object(t, branch)["type"] == "integer" {
				integer = true
			}
		}
		if !integer {
			t.Fatalf("建议金额 %s 应统一为整数分", field)
		}
	}
}

func TestAnalysisWarningsStayOutsideModelFacts(t *testing.T) {
	document := decoded(t)
	for _, name := range []string{"Finding", "FindingView"} {
		definition := schema(t, document, name)
		warnings := object(t, object(t, definition["properties"])["analysis_warnings"])
		if warnings["type"] != "array" || allowsNull(warnings) || !slices.Contains(stringsIn(t, definition["required"]), "analysis_warnings") {
			t.Fatalf("%s 应明确返回解析警告数组", name)
		}
	}
	facts := object(t, schema(t, document, "AnalysisFacts")["properties"])
	for _, key := range []string{"parse_warnings", "analysis_warnings"} {
		if _, exists := facts[key]; exists {
			t.Fatalf("内部解析元数据混入模型事实：%s", key)
		}
	}
}

func TestAllChannelKindsHaveClosedSecretInputsAndSafeViews(t *testing.T) {
	document := decoded(t)
	kinds := stringsIn(t, schema(t, document, "NotifyType")["enum"])
	channel := schema(t, document, "Channel")
	mapping := object(t, object(t, channel["discriminator"])["mapping"])
	if len(mapping) != len(kinds) {
		t.Fatal("渠道判别联合没有覆盖全部实际渠道")
	}
	for _, kind := range kinds {
		reference, ok := mapping[kind].(string)
		if !ok {
			t.Fatalf("渠道没有对应安全视图：%s", kind)
		}
		name := strings.TrimPrefix(reference, "#/components/schemas/")
		view := object(t, schema(t, document, name)["properties"])
		if object(t, view["type"])["const"] != kind {
			t.Fatalf("渠道类型没有固定：%s", kind)
		}
		for _, field := range []string{"secrets", "secret_enc", "uuid"} {
			if _, exists := view[field]; exists {
				t.Fatalf("%s 公开了秘密或内部身份 %s", kind, field)
			}
		}
		secretSet := object(t, view["secret_set"])
		if object(t, secretSet["additionalProperties"])["type"] != "boolean" {
			t.Fatalf("%s 应只返回秘密是否设置", kind)
		}
	}
	all := object(t, object(t, document["components"])["schemas"])
	for name, raw := range all {
		if !strings.HasPrefix(name, "Channel") || !strings.HasSuffix(name, "Secrets") {
			continue
		}
		for field, raw := range object(t, object(t, raw)["properties"]) {
			value := object(t, raw)
			if value["writeOnly"] != true || value["x-sensitive"] != true || allowsNull(value) {
				t.Fatalf("%s.%s 的秘密输入边界错误", name, field)
			}
		}
	}
	operation := object(t, object(t, object(t, document["paths"])["/api/v1/channels/{channel_id}"])["delete"])
	if _, exists := object(t, operation["responses"])["200"]; !exists {
		t.Fatal("渠道删除必须返回受影响任务，不能用 204 丢掉效果")
	}
}
