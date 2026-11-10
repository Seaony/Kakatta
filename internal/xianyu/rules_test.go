package xianyu

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestProtocolRulesRejectMalformedData(t *testing.T) {
	if _, err := decodeRules(bundledRules); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		path  string
		value any
	}{
		{"old version", "version", 1},
		{"unknown version", "version", 3},
		{"missing fields", "fields", nil},
		{"missing search fields", "fields.search", nil},
		{"missing item path", "fields.search.id", nil},
		{"empty item path", "fields.search.id", []string{}},
		{"blank path component", "fields.search.main", []string{"data", " "}},
		{"missing description paths", "fields.detail.descriptions", [][]string{}},
		{"empty description path", "fields.detail.descriptions", [][]string{{}}},
		{"missing seller cards", "fields.seller.cards", nil},
		{"missing seller credit path", "fields.seller.head.tag_level", nil},
		{"unknown search path", "fields.search.script", []string{"throw 1"}},
		{"missing items API", "api.user_items", ""},
		{"missing ratings API", "api.user_ratings", " "},
		{"missing ratings selector", "selectors.seller_ratings", ""},
		{"empty verify selector", "selectors.verify_masks", []string{""}},
		{"empty blocked text", "selectors.blocked_texts", []string{" "}},
		{"bad submit pattern", "text.region_submit_pattern", "["},
		{"zero home timeout", "timeouts_ms.home", 0},
		{"negative items timeout", "timeouts_ms.seller_items_response", -1},
		{"missing ratings timeout", "timeouts_ms.seller_ratings_response", nil},
		{"overflowing timeout", "timeouts_ms.detail", int64(math.MaxInt64)},
		{"unknown timeout", "timeouts_ms.script", 1},
		{"empty unconfirmed marker", "unconfirmed", []string{""}},
		{"script", "script", "throw 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(bundledRules, &document); err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(tc.path, ".")
			object := document
			for _, part := range parts[:len(parts)-1] {
				object = object[part].(map[string]any)
			}
			object[parts[len(parts)-1]] = tc.value
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeRules(body); err == nil {
				t.Fatal("接受了无效规则")
			}
		})
	}
	for _, body := range [][]byte{
		[]byte(`{"version":`),
		[]byte(`null`),
		append(append([]byte(nil), bundledRules...), []byte(`{}`)...),
		append(append([]byte(nil), bundledRules...), []byte(`null`)...),
		append(append([]byte(nil), bundledRules...), []byte(`garbage`)...),
	} {
		if _, err := decodeRules(body); err == nil {
			t.Fatal("接受了损坏规则或尾随内容")
		}
	}
}

func TestWebProtocolSnapshotsAreIndependent(t *testing.T) {
	want := WebProtocol()
	changed := WebProtocol()
	changed.API.Search = "changed"
	changed.Selectors.VerifyMasks[0] = "changed"
	changed.Selectors.BlockedTexts[0] = "changed"
	changed.Timeouts["home"] = 1
	changed.Unconfirmed[0] = "changed"
	if got := WebProtocol(); !reflect.DeepEqual(got, want) {
		t.Fatal("修改规则快照污染了后续读取")
	}
}

func TestProtocolJSONFieldPathChangesParsing(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(bundledRules, &document); err != nil {
		t.Fatal(err)
	}
	fields := document["fields"].(map[string]any)
	fields["search"].(map[string]any)["title"] = []string{"new_title"}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := decodeRules(body)
	if err != nil {
		t.Fatal(err)
	}
	previous := rules
	t.Cleanup(func() { rules = previous })
	rules = changed
	items, err := ParseSearch([]byte(`{"data":{"resultList":[{"data":{"item":{"main":{"exContent":{"itemId":"1","title":"旧标题","new_title":"新标题"}}}}}]}}`))
	if err != nil || len(items) != 1 || items[0].Title != "新标题" {
		t.Fatalf("JSON 字段路径未生效：%+v %v", items, err)
	}
}
