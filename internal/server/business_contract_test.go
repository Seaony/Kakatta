package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/api"
)

type apiOperation struct {
	Method, Path         string
	ID                   string                `json:"operationId"`
	Security             []map[string][]string `json:"security"`
	ImplementationStatus string                `json:"x-implementation-status"`
}

func testAPIOperations(t *testing.T) []apiOperation {
	t.Helper()
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(api.JSON(), &document); err != nil {
		t.Fatal(err)
	}
	var operations []apiOperation
	for path, item := range document.Paths {
		for method, raw := range item {
			if !strings.Contains("|get|post|patch|put|delete|head|options|", "|"+method+"|") {
				continue
			}
			var operation apiOperation
			if err := json.Unmarshal(raw, &operation); err != nil {
				t.Fatal(err)
			}
			operation.Method, operation.Path = strings.ToUpper(method), path
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].Method+operations[i].Path < operations[j].Method+operations[j].Path
	})
	return operations
}

var routeParameter = regexp.MustCompile(`\{[^}]+\}`)

func sampleRoutePath(pattern string) string { return routeParameter.ReplaceAllString(pattern, "1") }

func operationHandler(mux *http.ServeMux, operation apiOperation) (string, bool, error) {
	probe := &http.Request{Method: operation.Method, URL: &url.URL{Path: sampleRoutePath(operation.Path)}}
	_, pattern := mux.Handler(probe)
	registered := pattern != "" && pattern != "/"
	if operation.ImplementationStatus == "contract" {
		if registered {
			return pattern, false, fmt.Errorf("操作已有真实处理器，应移除契约阶段标记：%s %s", operation.Method, operation.Path)
		}
		return pattern, true, nil
	}
	if !registered {
		return pattern, false, fmt.Errorf("描述的操作没有真实处理器：%s %s", operation.Method, operation.Path)
	}
	return pattern, false, nil
}

func TestBusinessRoutesAndOpenAPIMatchBothWays(t *testing.T) {
	f := newBusinessFixture(t)
	routes := businessRoutes(f.server, f.service)
	declared := map[string]bool{}
	for _, route := range routes {
		if _, exists := declared[route.pattern]; exists {
			t.Fatalf("业务路由重复：%s", route.pattern)
		}
		declared[route.pattern] = false
		method, path, found := strings.Cut(route.pattern, " ")
		if !found {
			t.Fatal("业务路由缺少方法")
		}
		probe := &http.Request{Method: method, URL: &url.URL{Path: sampleRoutePath(path)}}
		if _, actual := f.server.mux.Handler(probe); actual != route.pattern {
			t.Fatalf("真实mux没有安装同一业务路由：%s，实际%s", route.pattern, actual)
		}
	}
	contracts := 0
	for _, operation := range testAPIOperations(t) {
		if operation.Path == "/healthz" {
			continue
		}
		pattern, contract, err := operationHandler(f.server.mux, operation)
		if err != nil {
			t.Error(err)
			continue
		}
		if contract {
			contracts++
			continue
		}
		if _, exists := declared[pattern]; exists {
			declared[pattern] = true
			if len(operation.Security) == 0 {
				t.Errorf("业务操作缺少鉴权声明：%s", operation.ID)
			}
		}
	}
	if contracts != 0 {
		t.Logf("另有 %d 个仅冻结契约且未注册的操作，未计作 HTTP 接线通过", contracts)
	}
	for pattern, documented := range declared {
		if !documented {
			t.Errorf("真实业务路由未在OpenAPI描述：%s", pattern)
		}
	}
	if err := f.server.RegisterService(f.service); err == nil {
		t.Fatal("重复装配应明确拒绝，不能重复注册或替换已有路由")
	}
	if err := f.server.RegisterService(nil); err == nil {
		t.Fatal("缺少真实业务服务不能装配")
	}
}

func TestContractMarkerCannotHideRegisteredHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", http.NotFound)
	mux.HandleFunc("GET /api/v1/test-implemented", func(http.ResponseWriter, *http.Request) {})
	for _, test := range []struct {
		name, path, status string
		contract, invalid  bool
	}{
		{"已注册却标契约", "/api/v1/test-implemented", "contract", false, true},
		{"已注册正常核对", "/api/v1/test-implemented", "", false, false},
		{"未注册仅作契约", "/api/v1/test-pending", "contract", true, false},
		{"未注册不能冒充已接线", "/api/v1/test-pending", "", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pattern, contract, err := operationHandler(mux, apiOperation{Method: http.MethodGet, Path: test.path, ImplementationStatus: test.status})
			if contract != test.contract || (err != nil) != test.invalid {
				t.Fatalf("阶段判断不符：pattern=%s contract=%t err=%v", pattern, contract, err)
			}
		})
	}
}
