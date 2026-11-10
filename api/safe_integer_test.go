package api_test

import (
	"encoding/json"
	"slices"
	"testing"
)

func integerNode(node map[string]any) bool {
	if node["type"] == "integer" || node["format"] == "int64" {
		return true
	}
	types, ok := node["type"].([]any)
	return ok && slices.Contains(types, any("integer"))
}

func integerBound(t *testing.T, node map[string]any, key string) int64 {
	t.Helper()
	value, ok := node[key].(json.Number)
	if !ok {
		t.Fatalf("整数字段缺少整数边界 %s：%v", key, node)
	}
	result, err := value.Int64()
	if err != nil {
		t.Fatalf("整数边界无法精确读取：%s=%s", key, value)
	}
	return result
}

func TestAllBusinessIntegersStayJSONSafe(t *testing.T) {
	const safe int64 = 9007199254740991
	document := decoded(t)
	count := 0
	walk(document, func(node map[string]any) {
		if !integerNode(node) {
			return
		}
		minimum, maximum := integerBound(t, node, "minimum"), integerBound(t, node, "maximum")
		if minimum < -safe || maximum > safe || minimum > maximum {
			t.Fatalf("整数边界超出安全范围或发生倒置：%v", node)
		}
		count++
	})
	if count == 0 {
		t.Fatal("没有检查到业务整数")
	}
	for _, name := range []string{"TaskCreate", "TaskPatch"} {
		account := object(t, object(t, schema(t, document, name)["properties"])["account_id"])
		bound := object(t, account["anyOf"].([]any)[0])
		if integerBound(t, bound, "minimum") != 1 || integerBound(t, bound, "maximum") != 12767 {
			t.Fatalf("%s 放宽了账号编号原有边界", name)
		}
	}
	port := object(t, object(t, schema(t, document, "ChannelEmailConfig")["properties"])["port"])
	if integerBound(t, port, "minimum") != 1 || integerBound(t, port, "maximum") != 65535 {
		t.Fatal("邮件端口原有边界被放宽")
	}
	paths := object(t, document["paths"])
	for _, rawPath := range paths {
		for _, rawOperation := range object(t, rawPath) {
			operation := object(t, rawOperation)
			parameters, _ := operation["parameters"].([]any)
			for _, raw := range parameters {
				parameter := object(t, raw)
				value := object(t, parameter["schema"])
				switch parameter["name"] {
				case "account_id":
					if integerBound(t, value, "minimum") != 1 || integerBound(t, value, "maximum") != 12767 {
						t.Fatal("账号路径参数原有边界被放宽")
					}
				case "limit":
					if integerBound(t, value, "minimum") != 1 || integerBound(t, value, "maximum") != 200 {
						t.Fatal("分页原有边界被放宽")
					}
				}
			}
		}
	}
}

func TestWebhookArbitraryJSONTravelsAsText(t *testing.T) {
	document := decoded(t)
	config := schema(t, document, "ChannelWebhookConfig")
	properties := object(t, config["properties"])
	for _, field := range []string{"query_params", "body_template"} {
		value := object(t, properties[field])
		if value["type"] != "string" || allowsNull(value) || slices.Contains(stringsIn(t, config["required"]), field) {
			t.Fatalf("Webhook %s 应为可省略、不可 null 的 JSON 文本", field)
		}
	}
	jsonValue := schema(t, document, "JsonValue")
	branches := jsonValue["oneOf"].([]any)
	if len(branches) != 6 {
		t.Fatal("任意 JSON 值的原有分支被修改")
	}
	for _, raw := range branches {
		value := object(t, raw)
		if value["type"] == "number" {
			if len(value) != 1 {
				t.Fatal("全局 JsonValue 的任意数字被加上了业务整数范围")
			}
			return
		}
	}
	t.Fatal("全局 JsonValue 丢失任意数字分支")
}
