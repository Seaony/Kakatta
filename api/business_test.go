package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func stringsIn(t *testing.T, raw any) []string {
	t.Helper()
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("应为文字数组：%T", raw)
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i], ok = value.(string)
		if !ok {
			t.Fatalf("数组成员不是文字：%T", value)
		}
	}
	return result
}

func allowsNull(node map[string]any) bool {
	if node["type"] == "null" {
		return true
	}
	if types, ok := node["type"].([]any); ok && slices.Contains(types, any("null")) {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if options, ok := node[key].([]any); ok {
			for _, option := range options {
				if value, ok := option.(map[string]any); ok && allowsNull(value) {
					return true
				}
			}
		}
	}
	return false
}

func TestBusinessInputPresenceAndSensitiveBoundaries(t *testing.T) {
	document := decoded(t)
	for _, name := range []string{"TaskPatch", "AccountPatch", "ProxyPatch", "ListingPatch"} {
		if got := stringsIn(t, schema(t, document, name)["required"]); len(got) != 0 {
			t.Fatalf("%s 的补丁成员不能强制出现：%v", name, got)
		}
	}
	if got := stringsIn(t, schema(t, document, "TaskCreate")["required"]); !slices.Equal(got, []string{"keyword"}) {
		t.Fatalf("新任务必填项错误：%v", got)
	}
	nullableTask := []string{"account_id", "min_price", "max_price", "budget_min", "budget_max", "region_province", "region_city", "region_district"}
	for _, name := range []string{"TaskCreate", "TaskPatch"} {
		properties := object(t, schema(t, document, name)["properties"])
		for field, raw := range properties {
			if allowsNull(object(t, raw)) != slices.Contains(nullableTask, field) {
				t.Fatalf("%s.%s 的 null 语义错误", name, field)
			}
		}
	}
	for name, want := range map[string][]string{
		"FindingReadPatch": {"is_read"}, "ItemSpecsRequest": {"specs", "brand_new"},
		"AnalysisUserOverrides": {"risks"}, "ExtraCheckRequest": {"extra_finding"},
	} {
		if got := stringsIn(t, schema(t, document, name)["required"]); !slices.Equal(got, want) {
			t.Fatalf("%s 没有保留明确输入：%v", name, got)
		}
	}
	if !allowsNull(object(t, object(t, schema(t, document, "ItemSpecsRequest")["properties"])["brand_new"])) {
		t.Fatal("用户确认的全新状态必须支持显式未知")
	}
	for name, forbidden := range map[string][]string{
		"AccountView": {"verify_url", "cookie", "proxy_url"},
		"Finding":     {"processing_revision", "reanalysis_origin", "notify_failures"},
		"ProxyView":   {"url", "url_enc", "uuid", "password"},
	} {
		properties := object(t, schema(t, document, name)["properties"])
		for _, field := range forbidden {
			if _, exists := properties[field]; exists {
				t.Fatalf("%s 不应公开 %s", name, field)
			}
		}
	}
	profile := schema(t, document, "AnalysisProfile")
	choices, ok := profile["oneOf"].([]any)
	if !ok || len(choices) != 2 || len(object(t, object(t, choices[0])["properties"])) != 0 {
		t.Fatal("关键词清单的空对象没有单独描述")
	}
	riskRequired := stringsIn(t, schema(t, document, "AnalysisProfileRisk")["required"])
	for _, optional := range []string{"name", "hint", "question"} {
		if slices.Contains(riskRequired, optional) {
			t.Fatalf("风险项的真实省略成员被强制出现：%s", optional)
		}
	}
}

func TestFindingSelectionCountsUniqueIDsAndKeepsUnknownKind(t *testing.T) {
	document := decoded(t)
	for _, name := range []string{"FindingSelection", "FindingBatchRequest"} {
		ids := object(t, object(t, schema(t, document, name)["properties"])["ids"])
		for _, key := range []string{"uniqueItems", "maxItems"} {
			if _, exists := ids[key]; exists {
				t.Fatalf("%s 不应按原数组拒绝重复或限制数量：%s", name, key)
			}
		}
		description, _ := ids["description"].(string)
		if !strings.Contains(description, "去重") || !strings.Contains(description, "1–200") {
			t.Fatalf("%s 没有说明去重后的数量限制", name)
		}
	}
	kind := object(t, object(t, schema(t, document, "TaskItemSpecs")["properties"])["kind"])
	if allowsNull(kind) {
		t.Fatal("未判断的商品种类应为空字符串，不能改成 null")
	}
	unknown := false
	for _, raw := range kind["anyOf"].([]any) {
		option := object(t, raw)
		if value, exists := option["const"]; exists && value == "" {
			unknown = true
		}
	}
	if !unknown {
		t.Fatal("商品种类没有保留空字符串未知值")
	}
}

type sourceField struct {
	name     string
	expr     ast.Expr
	required bool
}

func TestSharedDTOsMatchOwningTypes(t *testing.T) {
	document := decoded(t)
	definitions := map[string]ast.Expr{}
	for pkg, path := range map[string]string{
		"service": "../internal/service/types.go", "analysis": "../internal/analysis/types.go", "remote": "../internal/remote/session.go",
		"modelservice": "../internal/analysis/service/types.go", "client": "../internal/analysis/client/types.go",
		"notification": "../internal/notification/types.go", "notify": "../internal/notify/types.go",
	} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range group.Specs {
				if item, ok := spec.(*ast.TypeSpec); ok {
					definitions[pkg+"."+item.Name.Name] = item.Type
				}
			}
		}
	}
	var fields func(string, *ast.StructType) []sourceField
	fields = func(pkg string, node *ast.StructType) []sourceField {
		result := []sourceField{}
		for _, field := range node.Fields.List {
			if len(field.Names) == 0 {
				key := ""
				switch item := field.Type.(type) {
				case *ast.Ident:
					key = pkg + "." + item.Name
				case *ast.SelectorExpr:
					key = item.X.(*ast.Ident).Name + "." + item.Sel.Name
				}
				base, ok := definitions[key].(*ast.StructType)
				if !ok {
					t.Fatalf("嵌入类型无法解析：%s", key)
				}
				result = append(result, fields(strings.Split(key, ".")[0], base)...)
				continue
			}
			tag := ""
			if field.Tag != nil {
				value, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					t.Fatal(err)
				}
				tag = reflect.StructTag(value).Get("json")
			}
			parts := strings.Split(tag, ",")
			if parts[0] == "-" {
				continue
			}
			name := parts[0]
			if name == "" {
				name = field.Names[0].Name
			}
			result = append(result, sourceField{name: name, expr: field.Type, required: !slices.Contains(parts[1:], "omitempty")})
		}
		return result
	}
	all := object(t, object(t, document["components"])["schemas"])
	checked := 0
	for name, raw := range all {
		definition := object(t, raw)
		goType, ok := definition["x-go-type"].(string)
		input := false
		if !ok {
			goType, ok = definition["x-go-input-type"].(string)
			input = ok
		}
		if !ok {
			continue
		}
		owner, exists := definitions[goType]
		if !exists {
			t.Fatalf("描述引用了不存在的 Go 类型：%s", goType)
		}
		node, ok := owner.(*ast.StructType)
		if !ok {
			continue
		}
		if goType == "analysis.Profile" {
			definition = schema(t, document, "AnalysisProfileFields")
		}
		properties := object(t, definition["properties"])
		required := stringsIn(t, definition["required"])
		members := fields(strings.Split(goType, ".")[0], node)
		if len(properties) != len(members) {
			t.Fatalf("%s 和 %s 的公开字段数量不符", name, goType)
		}
		for _, field := range members {
			raw, exists := properties[field.name]
			if !exists || !input && slices.Contains(required, field.name) != field.required {
				t.Fatalf("%s.%s 没有遵守真实 JSON 成员及省略规则", name, field.name)
			}
			property := object(t, raw)
			if _, pointer := field.expr.(*ast.StarExpr); pointer != allowsNull(property) {
				t.Fatalf("%s.%s 的指针/null 口径不符", name, field.name)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("没有核对到任何共享类型")
	}
}

func TestBusinessRoutesAndDescriptionAgree(t *testing.T) {
	document := decoded(t)
	registered := map[string]bool{}
	pattern := regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) (/api/v1/\S+|/remote/\S+)$`)
	paths, err := filepath.Glob("../internal/server/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && pattern.MatchString(value) {
				registered[value] = true
			}
			return true
		})
	}
	remoteMount := "/remote/sessions/{session_id}/"
	if registered["GET "+remoteMount] {
		for _, suffix := range []string{"viewport", "vnc", "{asset_path}"} {
			registered["GET "+remoteMount+suffix] = true
		}
	}
	described := map[string]bool{}
	contracts := map[string]bool{}
	for path, raw := range object(t, document["paths"]) {
		if path == "/healthz" {
			continue
		}
		for method, operation := range object(t, raw) {
			route := strings.ToUpper(method) + " " + path
			described[route] = true
			if object(t, operation)["x-implementation-status"] == "contract" {
				contracts[route] = true
			}
		}
	}
	for route := range registered {
		if !described[route] {
			t.Errorf("服务已注册但描述缺失：%s", route)
		}
	}
	for route := range described {
		if registered[route] && contracts[route] {
			t.Errorf("正式路由已注册，应移除开发期契约标记并恢复严格核对：%s", route)
		}
		if !registered[route] && !contracts[route] {
			t.Errorf("描述存在但服务未注册：%s", route)
		}
	}
	if len(contracts) > 0 {
		t.Logf("另有 %d 个仅冻结契约的操作，未计作 HTTP 接线通过", len(contracts))
	}
	if len(registered) == 0 {
		t.Fatal("没有取得生产路由")
	}
	remote, err := os.ReadFile("../internal/remote/handler.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"GET /viewport", "GET /vnc", "GET /"} {
		if !strings.Contains(string(remote), strconv.Quote(route)) {
			t.Fatalf("远程子树入口已变化，需同步描述：%s", route)
		}
	}
}
