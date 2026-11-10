package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestBusinessErrorMappingAndReasonCopy(t *testing.T) {
	f := newServerFixture(t, true)
	next := f.clock.Load() + 5000
	var selected error
	b := &businessHandler{server: f.server}
	f.server.RegisterProtected("GET /api/v1/test-business-error", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.failure(w, r, selected)
	}))
	for kind, status := range map[service.ErrorKind]int{
		service.ErrorBadRequest: 400, service.ErrorInvalid: 422, service.ErrorNotFound: 404,
		service.ErrorConflict: 409, service.ErrorUnavailable: 503,
	} {
		t.Run(string(kind), func(t *testing.T) {
			args := map[string]any{"count": 3}
			business := &service.ServiceError{Kind: kind, Reason: locales.Message{Code: "run.account_gap", Args: args}, NextAllowedAt: &next,
				Fields: []settings.Issue{{Key: "task_id", Reason: message("http.invalid_request")}}}
			selected = business
			response := f.request(http.MethodGet, "/api/v1/test-business-error", "", true)
			requireStatus(t, response, status)
			value := decodeBody[ErrorEnvelope](t, response)
			if value.Error.Code != "run.account_gap" || value.Error.Args["next_allowed_at"] != float64(next) || len(value.Fields) != 1 || value.RequestID == "" {
				t.Fatal("业务错误的原因、字段和恢复时间没有完整透传")
			}
			if _, exists := business.Reason.Args["next_allowed_at"]; exists {
				t.Fatal("HTTP不能改写服务持有的原因参数")
			}
		})
	}
	selected = &service.ServiceError{Kind: service.ErrorConflict, Reason: message("run.no_account")}
	none := f.request(http.MethodGet, "/api/v1/test-business-error", "", true)
	if !strings.Contains(none.Body.String(), `"next_allowed_at":null`) {
		t.Fatal("没有恢复时间时应明确返回null")
	}
	for _, unknown := range []error{errors.New("数据库秘密标记"), &service.ServiceError{Kind: "unknown", Reason: message("数据库秘密标记")}} {
		selected = unknown
		response := f.request(http.MethodGet, "/api/v1/test-business-error", "", true)
		requireStatus(t, response, http.StatusInternalServerError)
		if errorCode(response) != "http.internal" || strings.Contains(response.Body.String(), "数据库秘密标记") {
			t.Fatal("未知错误不能回显内部内容")
		}
	}
}

func TestRawPatchTransportKeepsPresenceAndExactNumbers(t *testing.T) {
	f := newServerFixture(t, true)
	b := &businessHandler{server: f.server}
	var patch map[string]json.RawMessage
	f.server.RegisterProtected("POST /api/v1/test-raw-patch", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		patch, err = readPatch(w, r)
		b.reply(w, r, http.StatusOK, patch, err)
	}))
	valid := `{"nullable":null,"disabled":false,"amount":9223372036854775807,"object":{},"array":[]}`
	requireStatus(t, f.request(http.MethodPost, "/api/v1/test-raw-patch", valid, true), http.StatusOK)
	if string(patch["nullable"]) != "null" || string(patch["disabled"]) != "false" || string(patch["amount"]) != "9223372036854775807" || string(patch["array"]) != "[]" {
		t.Fatal("传输层丢失了null、false、空数组或整数精度")
	}
	if _, exists := patch["absent"]; exists {
		t.Fatal("缺失字段不能被填成零值")
	}
	for _, invalid := range []string{`null`, `[]`, `{"x":1,"x":2}`, `{"x":{"k":1,"k":2}}`, `{"x":1}{}`, "{\"x\":\"\xff\"}"} {
		requireStatus(t, f.request(http.MethodPost, "/api/v1/test-raw-patch", invalid, true), http.StatusBadRequest)
	}
	for count, status := range map[int]int{99: http.StatusOK, 100: http.StatusBadRequest} {
		body := `{"x":` + strings.Repeat("[", count) + "0" + strings.Repeat("]", count) + "}"
		requireStatus(t, f.request(http.MethodPost, "/api/v1/test-raw-patch", body, true), status)
	}
	requireStatus(t, f.request(http.MethodPost, "/api/v1/test-raw-patch", `{"x":"`+strings.Repeat("x", maxBody)+`"}`, true), http.StatusRequestEntityTooLarge)
}

func TestBusinessQueryAndPathParsing(t *testing.T) {
	for _, raw := range []string{"q=one&q=two", "other=value", "q=%ff", "q=%zz", "q=one;two", "%ff=value"} {
		r := httptest.NewRequest(http.MethodGet, "http://local.test/", nil)
		r.URL.RawQuery = raw
		if _, err := queryValues(r, "q"); err == nil {
			t.Fatalf("非法查询未被拒绝：%q", raw)
		}
	}
	for raw, valid := range map[string]bool{"true": true, "false": true, "1": false, "TRUE": false, "": false, "null": false} {
		value, err := queryBool(url.Values{"flag": {raw}}, "flag")
		if valid && (err != nil || value == nil || *value != (raw == "true")) || !valid && err == nil {
			t.Fatalf("布尔查询解析错误：%q", raw)
		}
	}
	if value, err := queryBool(url.Values{}, "flag"); err != nil || value != nil {
		t.Fatal("缺省布尔查询应保持未设置")
	}
	for _, raw := range []string{"0", "-1", "+1", "1e2", "9223372036854775808", ""} {
		r := httptest.NewRequest(http.MethodGet, "http://local.test/", nil)
		r.SetPathValue("task_id", raw)
		if _, err := pathID(r, "task_id"); err == nil {
			t.Fatalf("非法编号未被拒绝：%q", raw)
		}
	}
	if query, err := queryList(url.Values{}); err != nil || !reflect.DeepEqual(query, service.ListQuery{}) {
		t.Fatal("缺省分页应交给服务取默认值")
	}
	for _, raw := range []string{"0", "201", "1.5", ""} {
		if _, err := queryList(url.Values{"limit": {raw}}); err == nil {
			t.Fatalf("非法分页大小未被拒绝：%q", raw)
		}
	}
	query, err := queryList(url.Values{"limit": {"200"}, "cursor": {"opaque"}})
	if err != nil || query.Limit != 200 || query.Cursor != "opaque" {
		t.Fatal("分页边界或不透明游标被改变")
	}
	if value, err := queryTime(url.Values{"from_at": {"-1"}}, "from_at"); err != nil || value == nil || *value != -1 {
		t.Fatal("时间必须按Unix毫秒解析，不能套编号限制")
	}
	if _, err := queryTime(url.Values{"from_at": {"1.5"}}, "from_at"); err == nil {
		t.Fatal("时间不能接受非整数")
	}
}
