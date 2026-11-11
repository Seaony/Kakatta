package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func missingFieldRecorder() (*Runner, map[string][]any) {
	observed := map[string][]any{}
	return &Runner{Log: func(event string, fields map[string]any) {
		if event == "待确认观察" {
			if name, ok := fields["事项"].(string); ok && strings.HasSuffix(name, "_missing_fields") {
				observed[name] = append(observed[name], fields["结果"])
			}
		}
	}}, observed
}

func TestMissingSearchNicknameObservesOnlyThreeSamples(t *testing.T) {
	r, observed := missingFieldRecorder()
	body := []byte(`{"data":{"resultList":[{"data":{"item":{"main":{"exContent":{"itemId":"private-id-sentinel","userNickName":"","sellerName":"private-nick-sentinel","nicknameAlias":"private-alias-sentinel"},"sellerInfo":{"nickName":"private-other-nick"}}}}}]}}`)
	for range 8 {
		r.observeSearch(body)
	}
	samples := observed["search_nick_missing_fields"]
	if len(samples) != 3 {
		t.Fatalf("空昵称只应被动记录前三项结构，实际%d条", len(samples))
	}
	assertMissingFieldSafe(t, samples)
}

func TestMissingDetailCreditObservesOnlyThreeSamples(t *testing.T) {
	r, observed := missingFieldRecorder()
	body := []byte(`{"data":{"itemDO":{"desc":"","itemStatus":0},"sellerDO":{"zhimaLevelInfo":{"levelName":""},"creditInfo":{"creditText":"private-credit-sentinel"},"userId":"private-id-sentinel","private-account-sentinel":{"name":"private-nick-sentinel"},"123456789012345":{"number":7}}}}`)
	for range 8 {
		r.observeDetail(body)
	}
	samples := observed["detail_credit_missing_fields"]
	if len(samples) != 3 {
		t.Fatalf("空信用只应被动记录前三项结构，实际%d条", len(samples))
	}
	assertMissingFieldSafe(t, samples)
}

func TestPresentNicknameAndCreditDoNotProduceMissingFieldHints(t *testing.T) {
	r, observed := missingFieldRecorder()
	r.observeSearch([]byte(`{"data":{"resultList":[{"data":{"item":{"main":{"exContent":{"userNickName":"present-nickname"}}}}}]}}`))
	r.observeDetail([]byte(`{"data":{"itemDO":{"desc":"","itemStatus":0},"sellerDO":{"zhimaLevelInfo":{"levelName":"present-credit"}}}}`))
	if len(observed) != 0 {
		t.Fatal("正常字段不应记录缺失观察", observed)
	}
}

func assertMissingFieldSafe(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-", "private-account-sentinel", "123456789012345"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("结构观察泄露了字段值或动态账号键：%s", private)
		}
	}
}

func TestMissingFieldShapesDistinguishAbsentNullEmptyZeroAndFalse(t *testing.T) {
	for _, tc := range []struct {
		name, source, wantType string
		nonempty               bool
	}{
		{"missing", `{}`, "missing", false}, {"null", `{"levelName":null}`, "null", false}, {"empty", `{"levelName":""}`, "string", false}, {"zero", `{"levelName":0}`, "number", true}, {"false", `{"levelName":false}`, "boolean", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, observed := missingFieldRecorder()
			r.observeDetail([]byte(`{"data":{"itemDO":{"desc":""},"sellerDO":{"zhimaLevelInfo":` + tc.source + `}}}`))
			entries := observed["detail_credit_missing_fields"]
			if len(entries) != 1 {
				t.Fatal("应记录一次空信用结构", len(entries))
			}
			result := entries[0].(map[string]any)["sample_1"].(map[string]any)
			field := result["expected"].(missingFieldShape)
			if field.Type != tc.wantType || field.Nonempty != tc.nonempty {
				t.Fatalf("字段状态混淆：%+v", field)
			}
		})
	}
}

func TestOwnCountHintsAreScopedAndKeepKnownZero(t *testing.T) {
	r, observed := missingFieldRecorder()
	missing := []byte(`{"data":{"module":{"tabs":{"items":[{"name":"private-tab-sentinel","count":0}],"private-account-sentinel":{"number":7}}}}}`)
	r.observeSellerHead(missing)
	if len(observed) != 0 {
		t.Fatal("普通卖家头部不能触发自己的主页观察")
	}
	r.observeMissingOwnItemCount([]byte(`{"data":{"module":{"tabs":{"item":{"number":0}}}}}`), true)
	if len(observed) != 0 {
		t.Fatal("有效0不能被当作计数缺失")
	}
	for range 8 {
		r.observeMissingOwnItemCount(missing, false)
	}
	samples := observed["own_item_count_missing_fields"]
	if len(samples) != 3 {
		t.Fatal("自己的计数观察必须限前三项", len(samples))
	}
	assertMissingFieldSafe(t, samples)
	for i, sample := range samples {
		if _, ok := sample.(map[string]any)[fmt.Sprintf("sample_%d", i+1)]; !ok {
			t.Fatal("摘要需要独立保存每项结构")
		}
	}
}

func TestMissingOwnTabsStillRecordsNearbyModuleFields(t *testing.T) {
	r, observed := missingFieldRecorder()
	r.observeMissingOwnItemCount([]byte(`{"data":{"module":{"newItemStats":{"onSaleTotal":0},"base":{"displayName":"private-name-sentinel"}}}}`), false)
	samples := observed["own_item_count_missing_fields"]
	encoded, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "data.module.newItemStats.onSaleTotal") {
		t.Fatal("tabs不存在时仍需记录附近模块的字段结构")
	}
	assertMissingFieldSafe(t, samples)
}

func TestMissingNearbyFieldsAreBoundedAndHideDynamicKeys(t *testing.T) {
	root := map[string]any{"private-account-sentinel": "private-value", "123456789012345": json.Number("999999999999")}
	nested := root
	for range 20 {
		next := map[string]any{}
		nested["info"] = next
		nested = next
	}
	entries := make([]any, 100)
	for i := range entries {
		entries[i] = map[string]any{"number": json.Number("0"), "hasMore": false}
	}
	root["items"] = entries
	fields := missingNearbyFields(root, "data.module.tabs")
	if len(fields) > 40 {
		t.Fatal("结构超过字段上限", len(fields))
	}
	for _, field := range fields {
		if strings.Count(field.Path, ".") > 6 {
			t.Fatal("结构超过层数上限", field.Path)
		}
	}
	assertMissingFieldSafe(t, fields)
	var zero, negative bool
	for _, field := range fields {
		zero = zero || strings.HasSuffix(field.Path, ".number") && field.Type == "number" && field.Nonempty
		negative = negative || strings.HasSuffix(field.Path, ".hasMore") && field.Type == "boolean" && field.Nonempty
	}
	if !zero || !negative {
		t.Fatal("有效0和false必须保留类型与存在状态")
	}
}

func TestOwnProfileMissingHintsUseOnlyItsExistingResponse(t *testing.T) {
	f := newFake(t)
	r, _, ctx := startRunner(t, f)
	var requests atomic.Int64
	api := "/h5/" + rules.API.UserHead + "/1.0/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if req.URL.Path == "/personal" {
			fmt.Fprintf(w, `<!doctype html><title>本地主页</title><link rel="icon" href="data:,"><script>fetch(%q)</script>`, api)
			return
		}
		if req.URL.Path == api {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ret":["SUCCESS::ok"],"data":{"module":{"base":{"displayName":"private-name-sentinel"},"tabs":{"items":[{"name":"private-tab-sentinel","count":0}]}}}}`)
			return
		}
		http.NotFound(w, req)
	}))
	defer srv.Close()
	r.Site = Site{Home: srv.URL + "/", APIHost: strings.TrimPrefix(srv.URL, "http://")}
	_, observed := missingFieldRecorder()
	r.Log = func(event string, fields map[string]any) {
		if event == "待确认观察" && fields["事项"] == "own_item_count_missing_fields" {
			observed["own_item_count_missing_fields"] = append(observed["own_item_count_missing_fields"], fields["结果"])
		}
	}
	if _, err := r.OwnProfile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(observed["own_item_count_missing_fields"]) != 1 {
		t.Fatal("自己的资料响应必须触发观察")
	}
	if requests.Load() != 2 {
		t.Fatalf("只能有原有主页和接口请求，实际%d次", requests.Load())
	}
	assertMissingFieldSafe(t, observed)
}

func TestMissingFieldKeysKeepNewSchemaNamesButRejectDynamicIdentifiers(t *testing.T) {
	for _, key := range []string{"nicknameAlias", "newCreditLabel", "onSaleTotal", "_count2"} {
		if !missingFieldKey(key) {
			t.Fatal("替代路径的普通字段名必须保留", key)
		}
	}
	for _, key := range []string{"123456789012345", "account-name-sentinel", "账号昵称", strings.Repeat("x", 49), "f47ac10b-58cc-4372-a567-0e02b2c3d479", "f47ac10b58cc4372a5670e02b2c3d479"} {
		if missingFieldKey(key) {
			t.Fatal("不能记录动态账号或异常字段名")
		}
	}
	r, observed := missingFieldRecorder()
	r.observeSearch([]byte(`{"data":{"resultList":[{"data":{"item":{"main":{"exContent":{"userNickName":"","nicknameAlias":"private-alias-sentinel"}}}}}]}}`))
	samples := observed["search_nick_missing_fields"]
	encoded, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), ".nicknameAlias") {
		t.Fatal("不能把未知普通字段全部抹掉")
	}
	assertMissingFieldSafe(t, samples)
}
