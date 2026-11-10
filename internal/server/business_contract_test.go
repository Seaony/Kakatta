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
	expected := operation.Method + " " + operation.Path
	switch expected {
	case "GET /remote/sessions/{session_id}/viewport", "GET /remote/sessions/{session_id}/vnc", "GET /remote/sessions/{session_id}/{asset_path}":
		// 这些协议由鉴权后的远程会话处理器分发。
		expected = "GET /remote/sessions/{session_id}/"
	}
	registered := pattern == expected
	if operation.ImplementationStatus == "contract" {
		if registered {
			return pattern, false, fmt.Errorf("操作已有真实处理器，应移除契约阶段标记：%s %s", operation.Method, operation.Path)
		}
		return pattern, true, nil
	}
	if !registered {
		return pattern, false, fmt.Errorf("描述的操作没有匹配的处理器：%s %s，实际 %q", operation.Method, operation.Path, pattern)
	}
	return pattern, false, nil
}

func TestBusinessRoutesAndOpenAPIMatchBothWays(t *testing.T) {
	f := newBusinessFixture(t)
	f.registerTransferContracts(t)
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
		if operation.Method == http.MethodGet && operation.Path == "/healthz" {
			requireStatus(t, f.request(http.MethodGet, operation.Path, "", false), http.StatusOK)
			if len(operation.Security) != 0 {
				t.Error("健康探针不应要求业务凭据")
			}
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
		}
		t.Run(operation.Method+" "+operation.Path, func(t *testing.T) {
			path := sampleRoutePath(operation.Path)
			credentialsOnly := false
			switch operation.Method + " " + operation.Path {
			case "GET /api/v1/setup", "POST /api/v1/setup", "POST /api/v1/auth/login", "POST /api/v1/auth/totp":
				credentialsOnly = true
			}
			if credentialsOnly {
				if len(operation.Security) != 0 {
					t.Fatal("初始化或登录不应要求已有会话")
				}
			} else {
				if len(operation.Security) == 0 {
					t.Fatal("业务操作缺少鉴权声明")
				}
				response := f.request(operation.Method, path, "", false)
				requireStatus(t, response, http.StatusUnauthorized)
				if errorCode(response) != "auth.unauthenticated" {
					t.Fatal("匿名请求未被凭据检查拦截")
				}
			}
			switch operation.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				response := f.request(operation.Method, path, "", true, crossSiteTransferRequest)
				requireStatus(t, response, http.StatusForbidden)
				if errorCode(response) != "http.forbidden" {
					t.Fatal("跨站写入未被来源检查拦截")
				}
			}
		})
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
	mux.HandleFunc("GET /api/v1/tasks/{task_id}", func(http.ResponseWriter, *http.Request) {})
	for _, test := range []struct {
		name, path, status string
		contract, invalid  bool
	}{
		{"已注册却标契约", "/api/v1/test-implemented", "contract", false, true},
		{"已注册正常核对", "/api/v1/test-implemented", "", false, false},
		{"未注册仅作契约", "/api/v1/test-pending", "contract", true, false},
		{"未注册不能冒充已接线", "/api/v1/test-pending", "", false, true},
		{"参数路由不能代替静态路由", "/api/v1/tasks/export", "", false, true},
		{"参数路由不会把待接契约当已注册", "/api/v1/tasks/export", "contract", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pattern, contract, err := operationHandler(mux, apiOperation{Method: http.MethodGet, Path: test.path, ImplementationStatus: test.status})
			if contract != test.contract || (err != nil) != test.invalid {
				t.Fatalf("阶段判断不符：pattern=%s contract=%t err=%v", pattern, contract, err)
			}
		})
	}
}
