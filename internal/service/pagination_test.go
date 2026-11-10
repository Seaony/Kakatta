package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/limits"
)

func assertQueryError(t *testing.T, err error) {
	t.Helper()
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != ErrorBadRequest {
		t.Fatalf("应拒绝查询而不是回到第一页：%v", err)
	}
}

func TestPaginationBoundsAndDefaults(t *testing.T) {
	for _, test := range []struct{ given, want int64 }{{0, 50}, {1, 1}, {200, 200}} {
		q, err := normalizeListQuery(ListQuery{Limit: test.given})
		if err != nil || q.Limit != test.want {
			t.Fatalf("页大小%d：%+v %v", test.given, q, err)
		}
	}
	for _, q := range []ListQuery{{Limit: -1}, {Limit: 201}, {Limit: math.MaxInt64}, {Cursor: strings.Repeat("x", maxCursorBytes+1)}} {
		_, err := normalizeListQuery(q)
		assertQueryError(t, err)
	}
}

func TestCursorBindsResourceAndNormalizedFilterWithoutLosingIntegerPrecision(t *testing.T) {
	type filter struct {
		TaskID int64  `json:"task_id"`
		View   string `json:"view"`
	}
	type anchor struct {
		ID    int64  `json:"id"`
		Score *int64 `json:"score"`
	}
	score := int64(89)
	query := filter{TaskID: limits.MaxSafeInteger, View: "all"}
	want := anchor{ID: limits.MaxSafeInteger, Score: &score}
	token, err := encodeCursor("findings", query, want)
	if err != nil {
		t.Fatal(err)
	}
	var got anchor
	if err := decodeCursor(token, "findings", query, &got); err != nil || got.ID != want.ID || got.Score == nil || *got.Score != score {
		t.Fatalf("大整数锚点丢失：%+v %v", got, err)
	}
	assertQueryError(t, decodeCursor(token, "prices", query, &got))
	assertQueryError(t, decodeCursor(token, "findings", filter{TaskID: limits.MaxSafeInteger, View: "hidden"}, &got))
	if err := decodeCursor("", "findings", query, &got); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRejectsMalformedEnvelopeAndAnchor(t *testing.T) {
	type anchor struct {
		ID int64 `json:"id"`
	}
	token, err := encodeCursor("findings", ListQuery{}, anchor{ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(string(raw), `"version":1`, `"version":2`, 1),
		strings.Replace(string(raw), `"id":1`, `"id":"1"`, 1),
		strings.Replace(string(raw), `"id":1`, `"id":1,"sql":"DROP TABLE findings"`, 1),
		strings.Replace(string(raw), `"anchor":{"id":1}`, `"anchor":null`, 1),
		string(raw) + `{}`,
		strings.Replace(string(raw), `"version":1`, `"version":1,"extra":true`, 1),
	} {
		var got anchor
		assertQueryError(t, decodeCursor(base64.RawURLEncoding.EncodeToString([]byte(changed)), "findings", ListQuery{}, &got))
	}
	for _, bad := range []string{"not+base64", token + "=", strings.Repeat("a", maxCursorBytes+1)} {
		assertQueryError(t, decodeCursor(bad, "findings", ListQuery{}, &anchor{}))
	}
	if _, err := encodeCursor("findings", ListQuery{}, nil); err == nil {
		t.Fatal("空锚点不能生成可用游标")
	}
}

func TestCursorRejectsTamperedUnsafeIntegerAnchors(t *testing.T) {
	type taskAnchor struct {
		SortIndex int64 `json:"sort_index"`
		ID        int64 `json:"id"`
	}
	type idAnchor struct {
		ID int64 `json:"id"`
	}
	cases := []struct {
		scope, field string
		anchor       any
		signed       bool
	}{
		{"tasks", "sort_index", taskAnchor{ID: 1}, true}, {"tasks", "id", taskAnchor{ID: 1}, false},
		{"accounts", "id", idAnchor{ID: 1}, false}, {"proxies", "id", idAnchor{ID: 1}, false},
		{"findings", "id", findingCursor{ID: 1}, false}, {"findings", "found_at", findingCursor{ID: 1}, true},
		{"listings", "favorited_at", listingCursor{ItemID: "900719925474099200"}, true},
		{"listings", "last_seen_at", listingCursor{ItemID: "900719925474099200"}, true},
		{"prices:item", "observed_at", priceCursor{ID: 1}, true}, {"prices:item", "id", priceCursor{ID: 1}, false},
		{"runs", "started_at", runCursor{ID: 1}, true}, {"runs", "id", runCursor{ID: 1}, false},
		{"runs.steps", "seq", runStepCursor{Seq: 1}, false},
		{"blocked_sellers", "created_at", blockedSellerCursor{SellerID: "900719925474099200"}, true},
		{"system.events", "created_at", systemEventAnchor{ID: 1}, true}, {"system.events", "id", systemEventAnchor{ID: 1}, false},
		{"market_samples:1", "price", marketSampleAnchor{ItemID: "900719925474099200"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.scope+"/"+tc.field, func(t *testing.T) {
			token, err := encodeCursor(tc.scope, struct{}{}, tc.anchor)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := base64.RawURLEncoding.DecodeString(token)
			var envelope cursorEnvelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(envelope.Anchor, &fields); err != nil {
				t.Fatal(err)
			}
			values := []int64{limits.MaxSafeInteger, limits.MaxSafeInteger + 1, limits.MinSafeInteger - 1}
			if tc.signed {
				values = append(values, limits.MinSafeInteger, -1)
			}
			for _, value := range values {
				fields[tc.field], _ = json.Marshal(value)
				envelope.Anchor, _ = json.Marshal(fields)
				changed, _ := json.Marshal(envelope)
				var got = reflect.New(reflect.TypeOf(tc.anchor)).Interface()
				err := decodeCursor(base64.RawURLEncoding.EncodeToString(changed), tc.scope, struct{}{}, got)
				if value < limits.MinSafeInteger || value > limits.MaxSafeInteger {
					assertQueryError(t, err)
				} else if err != nil {
					t.Fatalf("合法锚点被拒绝：%d %v", value, err)
				} else {
					var preserved map[string]json.RawMessage
					encoded, _ := json.Marshal(got)
					json.Unmarshal(encoded, &preserved)
					if string(preserved[tc.field]) != string(fields[tc.field]) {
						t.Fatalf("整数锚点丢失精度：%s → %s", fields[tc.field], preserved[tc.field])
					}
				}
			}
		})
	}
}
