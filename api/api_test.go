package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/api"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func decoded(t *testing.T) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(api.JSON()))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("接口描述后还有内容：%v", err)
	}
	return value
}

func object(t *testing.T, value any) map[string]any {
	t.Helper()
	v, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("应为对象：%T", value)
	}
	return v
}

func schema(t *testing.T, document map[string]any, name string) map[string]any {
	t.Helper()
	components := object(t, document["components"])
	return object(t, object(t, components["schemas"])[name])
}

func walk(value any, visit func(map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		visit(v)
		for _, child := range v {
			walk(child, visit)
		}
	case []any:
		for _, child := range v {
			walk(child, visit)
		}
	}
}

func TestEmbeddedJSONIsCanonicalAndIndependent(t *testing.T) {
	want, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(api.JSON(), want) {
		t.Fatal("嵌入内容与唯一描述文件不一致")
	}
	copy := api.JSON()
	copy[0] = '!'
	if !bytes.Equal(api.JSON(), want) {
		t.Fatal("调用方修改了其他请求会读到的描述")
	}
	document := decoded(t)
	if document["openapi"] != "3.1.0" {
		t.Fatal("接口描述不是约定的 OpenAPI 3.1")
	}
}

func TestEveryOperationDeclaresItsAuthentication(t *testing.T) {
	document := decoded(t)
	public := map[string]string{
		"get /healthz":            "none",
		"get /api/v1/setup":       "initialization_status",
		"post /api/v1/setup":      "initialization_token",
		"post /api/v1/auth/login": "username_password",
		"post /api/v1/auth/totp":  "login_challenge_and_totp",
	}
	wantSecurity := []any{
		map[string]any{"CookieSession": []any{}},
		map[string]any{"SecureCookieSession": []any{}},
		map[string]any{"BearerToken": []any{}},
	}
	seen := map[string]bool{}
	for path, rawPath := range object(t, document["paths"]) {
		for method, rawOperation := range object(t, rawPath) {
			operation := object(t, rawOperation)
			id, ok := operation["operationId"].(string)
			if !ok || id == "" || seen[id] {
				t.Fatalf("操作名缺失或重复：%s %s %q", method, path, id)
			}
			seen[id] = true
			security, explicit := operation["security"]
			if !explicit {
				t.Fatalf("没有明确声明认证：%s %s", method, path)
			}
			if credential, public := public[method+" "+path]; public {
				if operation["x-authentication"] != credential || !reflect.DeepEqual(security, []any{}) {
					t.Fatalf("协议端点的凭据不符：%s %s", method, path)
				}
			} else if operation["x-authentication"] != "session_or_api_token" || !reflect.DeepEqual(security, wantSecurity) {
				t.Fatalf("业务端点没有同时接受受保护的会话和接口令牌：%s %s", method, path)
			}
			responses := object(t, operation["responses"])
			if object(t, responses["default"])["$ref"] != "#/components/responses/InternalError" {
				t.Fatalf("失败响应没有统一外层：%s %s", method, path)
			}
		}
	}
	security := object(t, object(t, document["components"])["securitySchemes"])
	for name, cookie := range map[string]string{"CookieSession": "kakatta_session", "SecureCookieSession": "__Host-kakatta_session"} {
		definition := object(t, security[name])
		if definition["in"] != "cookie" || definition["name"] != cookie {
			t.Fatalf("会话 Cookie 边界不符：%s", name)
		}
	}
}

func TestReferencesAndClosedObjects(t *testing.T) {
	document := decoded(t)
	walk(document, func(node map[string]any) {
		if raw, exists := node["$ref"]; exists {
			reference, ok := raw.(string)
			if !ok || !strings.HasPrefix(reference, "#/components/") {
				t.Fatalf("引用必须来自同一份描述：%v", raw)
			}
			var target any = document
			for _, part := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
				part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
				target = object(t, target)[part]
				if target == nil {
					t.Fatalf("引用不存在：%s", reference)
				}
			}
		}
		if rawProperties, fixed := node["properties"]; fixed {
			if node["additionalProperties"] != false {
				t.Fatal("固定结构没有拒绝未知属性")
			}
			properties := object(t, rawProperties)
			required, ok := node["required"].([]any)
			if !ok {
				t.Fatal("固定结构没有明确必填成员")
			}
			seen := map[string]bool{}
			for _, field := range required {
				name, ok := field.(string)
				if !ok || seen[name] {
					t.Fatalf("必填成员重复或名称无效：%v", field)
				}
				seen[name] = true
				if _, exists := properties[name]; !exists {
					t.Fatalf("必填字段没有描述：%v", field)
				}
			}
		}
	})
	values := schema(t, document, "SettingValues")
	if _, copied := values["properties"]; copied || object(t, values["additionalProperties"])["$ref"] != "#/components/schemas/JsonValue" {
		t.Fatal("设置值应共用 JsonValue，不能另抄一份设置键列表")
	}
}

func TestSecretsHaveTheRightDirection(t *testing.T) {
	document := decoded(t)
	requests := map[string][]string{
		"BackupExportRequest":   {"password"},
		"BackupRestoreUpload":   {"password"},
		"BackupConfirmRequest":  {"ticket"},
		"ProviderInput":         {"api_key"},
		"ProxyCreate":           {"url"},
		"ProxyPatch":            {"url"},
		"SetupRequest":          {"token", "password"},
		"LoginRequest":          {"password"},
		"TOTPLoginRequest":      {"challenge", "code"},
		"ChangePasswordRequest": {"old_password", "new_password"},
		"TOTPSetupRequest":      {"password"},
		"TOTPEnableRequest":     {"setup_token", "code"},
		"TOTPDisableRequest":    {"password", "code"},
	}
	responses := map[string][]string{
		"BackupRestorePreview": {"ticket"},
		"IssuedToken":          {"token"},
		"TOTPRequiredLogin":    {"challenge"},
		"TOTPSetup":            {"setup_token", "secret", "uri"},
	}
	for _, group := range []struct {
		fields    map[string][]string
		writeOnly bool
	}{{requests, true}, {responses, false}} {
		for name, fields := range group.fields {
			properties := object(t, schema(t, document, name)["properties"])
			for _, field := range fields {
				value := object(t, properties[field])
				if value["x-sensitive"] != true || (value["writeOnly"] == true) != group.writeOnly {
					t.Fatalf("敏感字段的读写方向错误：%s.%s", name, field)
				}
			}
		}
	}
	walk(document, func(node map[string]any) {
		if node["x-sensitive"] == true {
			for _, key := range []string{"default", "example", "examples"} {
				if _, exists := node[key]; exists {
					t.Fatalf("敏感字段不能带明文示例或默认值：%s", key)
				}
			}
		}
	})
	for name, forbidden := range map[string][]string{"SetupStatus": {"token", "username", "password"}, "TokenMetadata": {"token", "token_hash", "password", "secret"}} {
		properties := object(t, schema(t, document, name)["properties"])
		for _, field := range forbidden {
			if _, leaked := properties[field]; leaked {
				t.Fatalf("公开元数据暴露了敏感字段：%s.%s", name, field)
			}
		}
	}
}

func TestSettingSchemasMatchPublicJSON(t *testing.T) {
	document := decoded(t)
	check := func(name string, value any) map[string]any {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatal(err)
		}
		properties := object(t, schema(t, document, name)["properties"])
		if len(actual) != len(properties) {
			t.Fatalf("%s 字段数量与公开 JSON 不符：%v", name, actual)
		}
		for field := range actual {
			if _, exists := properties[field]; !exists {
				t.Fatalf("%s 漏了公开字段 %s", name, field)
			}
		}
		return actual
	}
	definitions := settings.Definitions(settings.Environment{TZ: "UTC"})
	if len(definitions) == 0 {
		t.Fatal("没有取得设置元数据，不能核对")
	}
	allowedKinds := object(t, object(t, schema(t, document, "SettingDefinition")["properties"])["kind"])["enum"].([]any)
	for _, definition := range definitions {
		actual := check("SettingDefinition", definition)
		if !slices.Contains(allowedKinds, actual["kind"]) || actual["options"] == nil || actual["recommendation"] == nil {
			t.Fatalf("元数据类型或空值口径不符：%s", definition.Key)
		}
		check("SettingRecommendation", definition.Recommendation)
		for _, value := range []locales.Message{definition.Label, definition.Why, definition.Risk, definition.Recommendation.Text} {
			if check("LocalizedMessage", value)["args"] == nil {
				t.Fatal("空参数必须是对象")
			}
		}
	}
	check("SettingsSnapshot", settings.Snapshot{Values: settings.Values{}, Sources: map[string]settings.Source{}, Warnings: []settings.Issue{}})
	check("FieldIssue", settings.Issue{Key: "general.timezone", Reason: locales.Message{Code: "settings.invalid"}})
}
