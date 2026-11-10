package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
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
	query := filter{TaskID: math.MaxInt64, View: "all"}
	want := anchor{ID: math.MaxInt64, Score: &score}
	token, err := encodeCursor("findings", query, want)
	if err != nil {
		t.Fatal(err)
	}
	var got anchor
	if err := decodeCursor(token, "findings", query, &got); err != nil || got.ID != want.ID || got.Score == nil || *got.Score != score {
		t.Fatalf("大整数锚点丢失：%+v %v", got, err)
	}
	assertQueryError(t, decodeCursor(token, "prices", query, &got))
	assertQueryError(t, decodeCursor(token, "findings", filter{TaskID: math.MaxInt64, View: "hidden"}, &got))
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
