package prompts

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func TestInputErrorKeepsStructuredField(t *testing.T) {
	var got *analysis.Error
	if err := inputError("price"); !errors.As(err, &got) || got.Message.Code != "analysis.prompts.invalid_input" || got.Message.Args["field"] != "price" {
		t.Fatalf("错误没有保留字段：%v", err)
	}
}

func TestUserJSONSortsEveryObjectWithoutLosingNumberPrecision(t *testing.T) {
	input := struct {
		Z json.RawMessage `json:"z"`
		A []any           `json:"a"`
	}{
		Z: json.RawMessage(`{"z":"<鱼>&","a":9007199254740993}`),
		A: []any{map[string]any{"b": json.Number("12345678901234567890.01200"), "a": int64(math.MaxInt64)}, json.Number("1e400")},
	}
	want := `{"a":[{"a":9223372036854775807,"b":12345678901234567890.01200},1e400],"z":{"a":9007199254740993,"z":"<鱼>&"}}`
	got, err := userJSON(input)
	if err != nil || got != want {
		t.Fatalf("键序、中文或数字精度改变：\n得到 %s\n应为 %s\n错误 %v", got, want, err)
	}
	again, err := userJSON(input)
	if err != nil || again != got || strings.HasSuffix(got, "\n") {
		t.Fatalf("相同输入应稳定且无尾换行：%q %v", again, err)
	}
}

func TestUserJSONKeepsArraysNullAndEscapedText(t *testing.T) {
	got, err := userJSON(map[string]any{"z": nil, "a": []any{"第三", "第一\n\"<&>"}})
	if err != nil || got != `{"a":["第三","第一\n\"<&>"],"z":null}` {
		t.Fatalf("数组顺序、空值或必要转义改变：%s %v", got, err)
	}
}

func TestUserJSONRejectsInvalidInputs(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	cases := map[string]any{
		"raw_unclosed":  json.RawMessage(`{"a":`),
		"raw_trailing":  json.RawMessage(`{"a":1} {}`),
		"raw_nested":    map[string]any{"a": json.RawMessage(`[1,]`)},
		"raw_duplicate": json.RawMessage(`{"a":1,"a":2}`),
		"raw_alias_key": json.RawMessage(`{"a":1,"\u0061":2}`),
		"raw_utf8":      json.RawMessage([]byte{'"', 0xff, '"'}),
		"nan":           math.NaN(),
		"positive_inf":  math.Inf(1),
		"negative_inf":  map[string]any{"a": math.Inf(-1)},
		"bad_number":    json.Number("01"),
		"nan_number":    json.Number("NaN"),
		"function":      func() {},
		"cycle":         cycle,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := userJSON(input)
			var structured *analysis.Error
			if got != "" || !errors.As(err, &structured) || structured.Message.Code != "analysis.prompts.invalid_input" || structured.Message.Args["field"] != "json" {
				t.Fatalf("非法输入不能静默改写：%q %v", got, err)
			}
		})
	}
}

func TestCentsNumberUsesExactIntegerConversion(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		want  string
	}{
		{0, "0"}, {1, "0.01"}, {-1, "-0.01"}, {10, "0.1"}, {-10, "-0.1"},
		{100, "1"}, {-100, "-1"}, {150, "1.5"}, {-150, "-1.5"}, {12345, "123.45"},
		{math.MaxInt64, "92233720368547758.07"}, {math.MinInt64, "-92233720368547758.08"},
	} {
		if got := centsNumber(tc.cents); string(got) != tc.want || !json.Valid([]byte(got)) {
			t.Fatalf("%d分：得到%s，应为%s", tc.cents, got, tc.want)
		}
	}
}

func TestFractionalCentsNumberShiftsDecimalWithoutRounding(t *testing.T) {
	for _, tc := range []struct {
		cents float64
		want  string
	}{
		{0, "0"}, {math.Copysign(0, -1), "0"}, {50, "0.5"}, {150, "1.5"},
		{123.5, "1.235"}, {-123.5, "-1.235"}, {0.5, "0.005"}, {-0.5, "-0.005"},
		{100.25, "1.0025"}, {12.3456789012345, "0.123456789012345"},
		{1e20, "1000000000000000000"},
	} {
		got, err := fractionalCentsNumber(tc.cents)
		if err != nil || string(got) != tc.want || !json.Valid([]byte(got)) {
			t.Fatalf("%s分：得到%s，应为%s，错误%v", strconv.FormatFloat(tc.cents, 'g', -1, 64), got, tc.want, err)
		}
	}
	for _, value := range []float64{math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64} {
		got, err := fractionalCentsNumber(value)
		if err != nil || !json.Valid([]byte(got)) || got == "0" {
			t.Fatalf("有限极值不能溢出或归零：%s %v", got, err)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got, err := fractionalCentsNumber(value)
		var structured *analysis.Error
		if got != "" || !errors.As(err, &structured) || structured.Message.Args["field"] != "cents" {
			t.Fatalf("非有限数应拒绝：%s %v", got, err)
		}
	}
}

func TestCloneProfileIsIndependentAndKeepsRevisions(t *testing.T) {
	original := analysis.Profile{
		Summary: "快照", NextNo: 8,
		Specs:        []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"256G"}, Edited: true}},
		Musts:        []analysis.Requirement{{ID: "m1", Text: "国行", Rev: 4}},
		Bonuses:      []analysis.Requirement{{ID: "b2", Text: "带发票", Rev: math.MaxInt64}},
		Risks:        []analysis.ProfileRisk{{ID: "c3", Name: "划痕", Hint: "屏幕", Question: "有划痕吗", Rev: 9, Edited: true}},
		AllowedKinds: []analysis.Kind{analysis.KindSale},
	}
	got := cloneProfile(original)
	if !reflect.DeepEqual(got, original) || got.Musts[0].Rev != 4 || got.Bonuses[0].Rev != math.MaxInt64 || got.Risks[0].Rev != 9 {
		t.Fatalf("快照不能变更版本或清单：%+v", got)
	}
	got.Specs[0].Name = "型号"
	got.Specs[0].Options[0] = "改变"
	got.Specs[0].Values[0] = "改变"
	got.Musts[0].Text = "改变"
	got.Musts[0].Rev++
	got.Bonuses[0].Text = "改变"
	got.Risks[0].Hint = "改变"
	got.Risks[0].Rev++
	got.AllowedKinds[0] = analysis.KindBroken
	if original.Specs[0].Name != "存储" || original.Specs[0].Options[0] != "128G" || original.Specs[0].Values[0] != "256G" || original.Musts[0].Text != "国行" || original.Musts[0].Rev != 4 || original.Bonuses[0].Text != "带发票" || original.Risks[0].Hint != "屏幕" || original.Risks[0].Rev != 9 || original.AllowedKinds[0] != analysis.KindSale {
		t.Fatal("修改快照影响了原清单")
	}
	for _, p := range []analysis.Profile{{}, {Specs: []analysis.ProfileSpec{}, Musts: []analysis.Requirement{}, Bonuses: []analysis.Requirement{}, Risks: []analysis.ProfileRisk{}, AllowedKinds: []analysis.Kind{}}} {
		if copy := cloneProfile(p); !reflect.DeepEqual(copy, p) {
			t.Fatalf("nil与空列表不能被规范化：%+v %+v", p, copy)
		}
	}
}

func TestCopyTaskIDHasNoAlias(t *testing.T) {
	if copyTaskID(nil) != nil {
		t.Fatal("未知task id应保留nil")
	}
	id := int64(42)
	copy := copyTaskID(&id)
	if copy == &id || *copy != id {
		t.Fatal("没有复制task id")
	}
	*copy = 8
	if id != 42 {
		t.Fatal("修改复制值影响原task id")
	}
}
