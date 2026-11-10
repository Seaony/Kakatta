package modelservice

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
)

func suggestionPointer[T any](value T) *T { return &value }

func TestConvertSuggestionsRoundsShortestDecimalToCents(t *testing.T) {
	for _, tc := range []struct {
		yuan  float64
		cents int64
	}{
		{0, 0}, {math.Copysign(0, -1), 0}, {0.0049, 0}, {0.005, 1},
		{1.004, 100}, {1.005, 101}, {2.675, 268}, {100.995, 10100},
		{2995, 299500}, {math.SmallestNonzeroFloat64, 0},
		{math.Nextafter(float64(math.MaxInt64)/100, 0), 9223372036854774000},
	} {
		t.Run(strconv.FormatFloat(tc.yuan, 'g', -1, 64), func(t *testing.T) {
			view, discarded := ConvertSuggestions(analysis.ProfileSuggestions{BudgetMin: &tc.yuan})
			if view.BudgetMin == nil || *view.BudgetMin != tc.cents || view.BudgetMax != nil || len(discarded) != 0 {
				t.Fatalf("十进制预算换算错误：%v => %+v %v，应为%d分", tc.yuan, view, discarded, tc.cents)
			}
		})
	}
}

func TestConvertSuggestionsDiscardsOnlyTheInvalidBudget(t *testing.T) {
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.001, -1, math.MaxFloat64, float64(math.MaxInt64) / 100} {
		view, discarded := ConvertSuggestions(analysis.ProfileSuggestions{BudgetMin: &invalid, BudgetMax: suggestionPointer(100.0), Keyword: suggestionPointer("保留搜索词"), ExcludeWords: []string{"壳"}})
		if view.BudgetMin != nil || view.BudgetMax == nil || *view.BudgetMax != 10000 || !reflect.DeepEqual(discarded, []string{"budget_min"}) || view.Keyword == nil || *view.Keyword != "保留搜索词" || !reflect.DeepEqual(view.ExcludeWords, []string{"壳"}) {
			t.Fatalf("无效下限不能丢掉其他建议：%v => %+v %v", invalid, view, discarded)
		}
		view, discarded = ConvertSuggestions(analysis.ProfileSuggestions{BudgetMin: suggestionPointer(10.0), BudgetMax: &invalid})
		if view.BudgetMin == nil || *view.BudgetMin != 1000 || view.BudgetMax != nil || !reflect.DeepEqual(discarded, []string{"budget_max"}) {
			t.Fatalf("无效上限不能丢掉有效下限：%v => %+v %v", invalid, view, discarded)
		}
	}
}

func TestConvertSuggestionsDiscardsBothReversedBudgets(t *testing.T) {
	for _, bounds := range [][2]float64{{200, 100}, {1.004, 1.003}} {
		view, discarded := ConvertSuggestions(analysis.ProfileSuggestions{BudgetMin: &bounds[0], BudgetMax: &bounds[1]})
		if view.BudgetMin != nil || view.BudgetMax != nil || !reflect.DeepEqual(discarded, []string{"budget_min", "budget_max"}) {
			t.Fatalf("倒置预算应都不建议：%v => %+v %v", bounds, view, discarded)
		}
	}
}

func TestConvertSuggestionsClonesAndMergesPrivateDiagnostics(t *testing.T) {
	keyword := "原搜索词"
	source := analysis.ProfileSuggestions{BudgetMin: suggestionPointer(-1.0), Keyword: &keyword, ExcludeWords: []string{"原排除词"}, DiscardedBudgets: []string{"budget_max", "secret-input", "budget_min", "budget_max"}}
	view, discarded := ConvertSuggestions(source)
	if !reflect.DeepEqual(discarded, []string{"budget_min", "budget_max"}) {
		t.Fatalf("诊断必须固定字段、顺序且去重：%v", discarded)
	}
	view.ExcludeWords[0] = "改后的排除词"
	*view.Keyword = "改后的搜索词"
	discarded[0] = "changed"
	if source.ExcludeWords[0] != "原排除词" || keyword != "原搜索词" || source.DiscardedBudgets[0] != "budget_max" {
		t.Fatal("转换结果修改了输入来源")
	}
	view, _ = ConvertSuggestions(analysis.ProfileSuggestions{})
	if view.ExcludeWords == nil {
		t.Fatal("公开排除词应为空数组")
	}
	encoded, err := json.Marshal(GeneratedDraft{Profile: analysis.Profile{}, Suggestions: view})
	if err != nil || !strings.Contains(string(encoded), `"suggestions":{"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[]}`) || strings.Contains(string(encoded), "discarded") || strings.Contains(string(encoded), "request") {
		t.Fatalf("公开结果泄漏内部诊断或缺安全建议：%s %v", encoded, err)
	}
}

func TestConvertSuggestionsKeepsParserDiscardsOutOfUIWarnings(t *testing.T) {
	body := `{"summary":"会帮你找：笔记本","specs":[],"musts":[],"bonuses":[],"risks":[],"allowed_kinds":[],"budget_min":1e309,"budget_max":100.995,"keyword":"笔记本","exclude_words":["求购"]}`
	generated, warnings, err := analysis.ParseGeneratedProfile([]byte(body))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("坏预算不能让清单失效或进入界面警告：%v %v", err, warnings)
	}
	profile, suggestions, warnings, err := analysis.MergeGeneratedProfile(analysis.Profile{}, generated, "笔记本", "")
	if err != nil {
		t.Fatal(err)
	}
	view, discarded := ConvertSuggestions(suggestions)
	if profile.Summary != "会帮你找：笔记本" || view.BudgetMin != nil || view.BudgetMax == nil || *view.BudgetMax != 10100 || !reflect.DeepEqual(discarded, []string{"budget_min"}) || len(warnings) != 0 {
		t.Fatalf("解析、合并和公开换算未保留清单/私有诊断：%+v %+v %v %v", profile, view, discarded, warnings)
	}
}
