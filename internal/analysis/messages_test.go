package analysis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestProductionMessagesFormatInBothLanguages(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		checked++
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, b, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Error(err)
				return true
			}
			if strings.HasPrefix(s, "analysis.") && !strings.HasSuffix(s, ".") {
				codes[s] = true
			}
			return true
		})
	}
	if checked == 0 || len(codes) == 0 {
		t.Fatal("未检查任何生产消息")
	}
	for _, d := range RiskDictionary() {
		if d.Question != "" {
			codes["analysis.question."+d.ID] = true
		}
	}
	for _, kind := range []Kind{KindWanted, KindAccessory, KindRecycle, KindRental, KindRepair, KindBox, KindMulti, KindBroken, KindOther} {
		codes["analysis.junk."+string(kind)] = true
	}
	args := map[string]any{"value": "256G", "field": "checks", "item": "id_lock", "no": 1, "text": "国行", "name": "自定义名称", "word": "有锁", "min": "100", "max": "200", "days": 2, "count": 2, "words": "q1、mini", "kind": "wanted", "reason": "模型原话", "timezone": "UTC", "id": "id_lock", "day": "2026-11-10", "evidence": "卖家原话", "attitude": "confirm", "score": 89, "grade": "good", "limit": 89, "question": "用户原问题"}
	for code := range codes {
		for _, lang := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
			t.Run(string(lang)+"/"+code, func(t *testing.T) {
				got, err := locales.Format(lang, locales.Message{Code: code, Args: args})
				if err != nil || strings.TrimSpace(got) == "" {
					t.Fatalf("%s：%q %v", code, got, err)
				}
			})
		}
	}
}

func TestActualDayReasonsCarryExplicitPluralCount(t *testing.T) {
	published := FirstLayer(FirstLayerInput{NowMS: 2 * 86400000, PublishDays: 1, PublishedAt: ptr(int64(0))}).Reason
	seller := JudgeSeller(SellerInput{RegistrationDays: ptr(1)}, nil, "").Reasons[0]
	messages := []locales.Message{*published, seller}
	for _, m := range messages {
		if m.Args["count"] == nil || m.Args["days"] == nil {
			t.Fatalf("天数缺显式count：%+v", m)
		}
		got, err := locales.Format(locales.En, m)
		if err != nil || !strings.Contains(got, "1 day") || strings.Contains(got, "1 days") {
			t.Fatalf("英文单数：%q %v", got, err)
		}
		m.Args["days"], m.Args["count"] = 2, 2
		got, err = locales.Format(locales.En, m)
		if err != nil || !strings.Contains(got, "2 days") {
			t.Fatalf("英文复数：%q %v", got, err)
		}
	}
}

func TestActualFairPriceReasonsCarryWindowCount(t *testing.T) {
	in := marketInput()
	in.Profile.Specs = []ProfileSpec{{Name: "型号", Options: []string{"A"}}, {Name: "容量", Options: []string{"128", "256"}}}
	for i := 0; i < 6; i++ {
		capacity := "128"
		if i < 2 {
			capacity = "256"
		}
		row := valuationObservation(strconv.Itoa(i), "2026-11-10", 100)
		row.Specs = []SpecValue{{Name: "型号", Value: ptr("A")}, {Name: "容量", Value: ptr(capacity)}}
		in.Observations = append(in.Observations, row)
	}
	groups := CalculateMarket(in).Groups
	if len(groups) != 2 {
		t.Fatal(groups)
	}
	for _, g := range groups {
		m := g.Fair.Basis
		if m.Code != "analysis.fair_relaxed" || m.Args["count"] != 30 || m.Args["days"] != 30 {
			t.Fatalf("放宽依据缺count：%+v", m)
		}
		if _, err := locales.Format(locales.En, m); err != nil {
			t.Fatal(err)
		}
	}
	in.Profile = Profile{}
	in.Observations = in.Observations[:5]
	m := CalculateMarket(in).Groups[0].Fair.Basis
	if m.Code != "analysis.fair_same_config" || m.Args["count"] != 14 || m.Args["days"] != 14 {
		t.Fatalf("原配置依据缺count：%+v", m)
	}
	if _, err := locales.Format(locales.En, m); err != nil {
		t.Fatal(err)
	}
}
