package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func missingValueSample(t *testing.T, observed map[string][]any, name string, index int) map[string]any {
	t.Helper()
	samples := observed[name]
	if len(samples) <= index {
		t.Fatalf("缺少第%d项观察，实际%d项", index+1, len(samples))
	}
	data, err := json.Marshal(samples[index])
	if err != nil {
		t.Fatal(err)
	}
	var sample map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&sample); err != nil {
		t.Fatal(err)
	}
	return sample[fmt.Sprintf("sample_%d", index+1)].(map[string]any)
}

func missingDetailValue(t *testing.T, sample map[string]any, suffix string) map[string]any {
	t.Helper()
	values, _ := sample["values"].([]any)
	for _, raw := range values {
		field, _ := raw.(map[string]any)
		if field["path"] == "data.sellerDO."+suffix {
			return field
		}
	}
	t.Fatal("缺少固定白名单字段形状", suffix)
	return nil
}

func TestMissingNicknameKeepsAuthorizedSwitchScalars(t *testing.T) {
	for _, value := range []any{false, true, json.Number("0"), json.Number("2"), "false", "UNKNOWN_FLAG", "  原始标记  "} {
		t.Run(fmt.Sprintf("%T_%v", value, value), func(t *testing.T) {
			r, observed := missingFieldRecorder()
			main := map[string]any{"exContent": map[string]any{"userNickName": ""}, "clickParam": map[string]any{"args": map[string]any{"HideUserInfo": value, "seller_id": "private-id-sentinel"}}}
			r.observeMissingSearchNick(main)
			sample := missingValueSample(t, observed, "search_nick_missing_fields", 0)
			field, _ := sample["hide_user_info"].(map[string]any)
			if field["path"] != "data.resultList[].data.item.main.clickParam.args.HideUserInfo" || !reflect.DeepEqual(field["value"], value) {
				t.Fatalf("指定开关标量应按原型原值记录：%+v", field)
			}
			assertMissingFieldSafe(t, observed)
		})
	}
}

func TestMissingCreditKeepsOnlyAuthorizedPublicValues(t *testing.T) {
	r, observed := missingFieldRecorder()
	const icon = "https://static.example.invalid/credit/lv-3.png?variant=blue"
	seller := map[string]any{
		"idleFishCreditTag": map[string]any{"text": "private-credit-sentinel", "iconUrl": icon, "trackParams": map[string]any{"sellerLevel": "LEVEL_V2", "pageUserId": "private-id-sentinel"}},
		"zhimaAuth":         false, "itemCount": json.Number("0"), "hasSoldNumInteger": "12件", "avgReply30dLong": json.Number("1.5"), "newGoodRatioRate": "96.8%", "lastVisitTime": "18小时前",
		"sellerId": "private-id-sentinel", "nickName": "private-nick-sentinel", "apiKey": "private-token-sentinel",
	}
	r.observeMissingDetailCredit(map[string]any{"data": map[string]any{"sellerDO": seller}})
	sample := missingValueSample(t, observed, "detail_credit_missing_fields", 0)
	for suffix, want := range map[string]any{
		"idleFishCreditTag.iconUrl": icon, "idleFishCreditTag.trackParams.sellerLevel": "LEVEL_V2", "zhimaAuth": false,
		"itemCount": json.Number("0"), "hasSoldNumInteger": "12件", "avgReply30dLong": json.Number("1.5"), "newGoodRatioRate": "96.8%", "lastVisitTime": "18小时前",
	} {
		if field := missingDetailValue(t, sample, suffix); !reflect.DeepEqual(field["value"], want) {
			t.Fatalf("指定公开指标应保留原型原值，路径%s，实际%+v", suffix, field)
		}
	}
	assertMissingFieldSafe(t, observed)
}

func TestMissingCreditPublicTextBoundaries(t *testing.T) {
	for _, field := range []string{"itemCount", "hasSoldNumInteger", "avgReply30dLong", "newGoodRatioRate", "lastVisitTime"} {
		for _, length := range []int{64, 65} {
			t.Run(fmt.Sprintf("%s_%d", field, length), func(t *testing.T) {
				r, observed := missingFieldRecorder()
				value := strings.Repeat("字", length)
				r.observeMissingDetailCredit(map[string]any{"data": map[string]any{"sellerDO": map[string]any{field: value}}})
				got := missingDetailValue(t, missingValueSample(t, observed, "detail_credit_missing_fields", 0), field)
				actual, recorded := got["value"]
				if length == 64 && (!recorded || actual != value) || length == 65 && recorded {
					t.Fatal("公开指标文本必须以字符计数，超长只留形状")
				}
			})
		}
	}
	for _, length := range []int{4096, 4097} {
		t.Run(fmt.Sprintf("icon_%d", length), func(t *testing.T) {
			r, observed := missingFieldRecorder()
			value := strings.Repeat("a", length)
			r.observeMissingDetailCredit(map[string]any{"data": map[string]any{"sellerDO": map[string]any{"idleFishCreditTag": map[string]any{"iconUrl": value}}}})
			got := missingDetailValue(t, missingValueSample(t, observed, "detail_credit_missing_fields", 0), "idleFishCreditTag.iconUrl")
			actual, recorded := got["value"]
			if length == 4096 && (!recorded || actual != value) || length == 4097 && recorded {
				t.Fatal("图标原址不能截半或脱敏，超过上限只留形状")
			}
		})
	}
}

func TestMissingPublicFieldsNeverCopyObjectsOrPrivateNeighbors(t *testing.T) {
	for _, value := range []any{map[string]any{"cookie": "private-cookie-sentinel", "123456789012345": "private-id-sentinel"}, []any{"private-nick-sentinel"}, nil, json.Number("private-invalid-number")} {
		r, observed := missingFieldRecorder()
		r.observeMissingSearchNick(map[string]any{"clickParam": map[string]any{"args": map[string]any{"HideUserInfo": value}}})
		seller := map[string]any{"idleFishCreditTag": map[string]any{"text": "private-credit-sentinel", "iconUrl": value, "trackParams": map[string]any{"sellerLevel": value, "pageUserId": "private-id-sentinel"}}, "zhimaAuth": value}
		for _, field := range []string{"itemCount", "hasSoldNumInteger", "avgReply30dLong", "newGoodRatioRate", "lastVisitTime"} {
			seller[field] = value
		}
		r.observeMissingDetailCredit(map[string]any{"data": map[string]any{"sellerDO": seller}})
		search := missingValueSample(t, observed, "search_nick_missing_fields", 0)
		if field, _ := search["hide_user_info"].(map[string]any); field["value"] != nil {
			t.Fatal("开关的对象值不得复制到日志")
		}
		detail := missingValueSample(t, observed, "detail_credit_missing_fields", 0)
		values, present := detail["values"].([]any)
		if !present {
			t.Fatal("缺少固定公开字段列表")
		}
		for _, raw := range values {
			if raw.(map[string]any)["value"] != nil {
				t.Fatal("白名单也不能复制动态对象、数组或无效数字")
			}
		}
		assertMissingFieldSafe(t, observed)
	}
}

func TestMissingZhimaAuthenticationOnlyRecordsBooleans(t *testing.T) {
	for _, value := range []any{false, true, "true", json.Number("1")} {
		r, observed := missingFieldRecorder()
		r.observeMissingDetailCredit(map[string]any{"data": map[string]any{"sellerDO": map[string]any{"zhimaAuth": value}}})
		field := missingDetailValue(t, missingValueSample(t, observed, "detail_credit_missing_fields", 0), "zhimaAuth")
		actual, present := field["value"]
		_, boolean := value.(bool)
		if present != boolean || boolean && actual != value {
			t.Fatal("芝麻认证只记录真实布尔值，不能猜字符串或数字映射")
		}
	}
}

func TestDedicatedCreditTagShapeIsCompleteAndValueFree(t *testing.T) {
	r, observed := missingFieldRecorder()
	tag := map[string]any{"trackParams": map[string]any{"sellerLevel": "LEVEL_V2", "pageUserId": "private-id-sentinel"}, "123456789012345": "private-id-sentinel"}
	for i := range 80 {
		tag[fmt.Sprintf("field%d", i)] = "private-text-sentinel"
	}
	r.observeMissingDetailCredit(map[string]any{"data": map[string]any{"sellerDO": map[string]any{"idleFishCreditTag": tag}}})
	sample := missingValueSample(t, observed, "detail_credit_missing_fields", 0)
	fields, _ := sample["credit_tag_nearby"].([]any)
	if len(fields) <= 80 {
		t.Fatalf("定点信用结构不得受整体40项截断，实际%d项", len(fields))
	}
	for _, raw := range fields {
		field := raw.(map[string]any)
		if _, copied := field["value"]; copied {
			t.Fatal("完整结构本身仍不能复制字段值")
		}
	}
	assertMissingFieldSafe(t, observed)
}

func TestAddedPublicValuesKeepThreeSamplesPerKind(t *testing.T) {
	r, observed := missingFieldRecorder()
	for i := range 8 {
		r.observeMissingSearchNick(map[string]any{"clickParam": map[string]any{"args": map[string]any{"HideUserInfo": fmt.Sprint(i)}}})
		r.observeMissingDetailCredit(map[string]any{"data": map[string]any{"sellerDO": map[string]any{"itemCount": json.Number(fmt.Sprint(i))}}})
		r.observeMissingOwnItemCount([]byte(`{"data":{"module":{}}}`), false)
	}
	for _, name := range []string{"search_nick_missing_fields", "detail_credit_missing_fields", "own_item_count_missing_fields"} {
		if len(observed[name]) != 3 {
			t.Fatal("新增标量不能增加定点样本额度", name, len(observed[name]))
		}
	}
	for i := range 3 {
		search := missingValueSample(t, observed, "search_nick_missing_fields", i)
		field, _ := search["hide_user_info"].(map[string]any)
		if field["value"] != fmt.Sprint(i) {
			t.Fatal("前三个开关值应分别保留")
		}
	}
}

func TestSearchSellerIdentifierObservesShapeIncludingSnakeCase(t *testing.T) {
	var result map[string]any
	r := &Runner{Log: func(event string, fields map[string]any) {
		if event == "待确认观察" && fields["事项"] == "search_seller_id" {
			result, _ = fields["结果"].(map[string]any)
		}
	}}
	r.observeSearch([]byte(`{"data":{"resultList":[{"data":{"item":{"main":{"clickParam":{"args":{"seller_id":"private-id-sentinel"}},"123456789012345":{"sellerId":"private-nick-sentinel"}}}}}]}}`))
	encoded, _ := json.Marshal(result)
	if result["found"] != true || !strings.Contains(string(encoded), "clickParam.args.seller_id") || !strings.Contains(string(encoded), `"type":"string"`) || !strings.Contains(string(encoded), `"nonempty":true`) {
		t.Fatal("必须识别seller_id，并只保留路径、类型和是否非空")
	}
	assertMissingFieldSafe(t, result)
}
