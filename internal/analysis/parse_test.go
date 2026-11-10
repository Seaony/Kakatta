package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func factBody(changes map[string]any) []byte {
	m := map[string]any{"specs": []any{}, "brand_new": nil, "musts": []any{}, "bonuses": []any{}, "checks": []any{}, "seller_signals": []any{}, "price_note": nil, "extra_findings": []any{}, "adjust": map[string]any{"value": 0, "reason": ""}, "summary": "正常"}
	for k, v := range changes {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}
func TestTitleParserFailuresAndSingleItemFallback(t *testing.T) {
	b := []byte(`prose {bad} then {"items":[{"no":1,"kind":"sale","specs":[],"brand_new":null,"verdict":"pass","reason":"字符串里有 } 和 {"},{"no":2,"kind":"mystery","specs":[],"brand_new":null,"verdict":"pass","reason":""},{"no":1,"kind":"wanted","specs":[],"brand_new":null,"verdict":"pass","reason":"重复"}]} tail`)
	got, w, err := ParseTitleScreen(b, 3)
	if err != nil || len(w) < 2 || len(got.Items) != 3 || got.Items[0].Kind != KindSale || got.Items[1].Kind != KindSale || got.Items[2].Kind != KindSale {
		t.Fatal(got, w, err)
	}
	for _, b := range []string{`{}`, `{"items":null}`, `{"items":{}}`} {
		if _, _, err := ParseTitleScreen([]byte(b), 1); err == nil {
			t.Error("accepted", b)
		}
	}
	if _, _, err := ParseTitleScreen([]byte(`{"items":[]}`), 21); err == nil {
		t.Fatal("accepted >20")
	}
}

func TestFactsRequiredShapeAndTolerantOptionalFields(t *testing.T) {
	for _, change := range []map[string]any{{"checks": nil}, {"checks": map[string]any{}}, {"checks": []any{map[string]any{"key": "x", "status": "bad"}}}, {"adjust": map[string]any{"value": "2"}}, {"summary": 3}} {
		if _, _, err := ParseFacts(factBody(change), Profile{}, EvidenceOriginals{}); err == nil {
			t.Error("accepted", change)
		}
	}
	got, _, err := ParseFacts(factBody(map[string]any{"specs": 42, "musts": "bad", "bonuses": nil, "seller_signals": true, "extra_findings": []any{"bad"}, "adjust": map[string]any{"value": 2.5, "reason": "配件全"}}), Profile{}, EvidenceOriginals{})
	if err != nil || got.Adjust.Value != 3 || len(got.Specs) != 0 {
		t.Fatal(got, err)
	}
	for _, tt := range []struct {
		value  float64
		reason string
		want   int
	}{{99, "依据", 8}, {-99, "依据", -15}, {-2.5, "依据", -3}, {3, "  ", 0}} {
		got, _, err := ParseFacts(factBody(map[string]any{"adjust": map[string]any{"value": tt.value, "reason": tt.reason}}), Profile{}, EvidenceOriginals{})
		if err != nil || got.Adjust.Value != tt.want {
			t.Fatal(got, err)
		}
	}
}

func TestFactsEvidenceNFKCAndDowngrades(t *testing.T) {
	p := Profile{Specs: []ProfileSpec{{Name: "容量", Options: []string{"256G"}}}, Musts: []Requirement{{ID: "m1"}, {ID: "m2"}, {ID: "m3"}}, Bonuses: []Requirement{{ID: "b4"}}, Risks: []ProfileRisk{{ID: "repaired"}, {ID: "id_lock"}, {ID: "c5", Name: "亮斑"}, {ID: "c6", Name: "未知"}, {ID: "c7", Name: "来源缺失"}, {ID: "seller_new"}}}
	change := map[string]any{
		"specs":          []any{map[string]any{"name": "容量", "value": "２５６Ｇ"}, map[string]any{"name": "外来", "value": "x"}},
		"musts":          []any{map[string]any{"id": "m1", "status": "met", "source": "text", "evidence": "国行，未拆"}, map[string]any{"id": "m2", "status": "unmet", "source": "image", "evidence": "第1张图：非国行"}, map[string]any{"id": "m3", "status": "unmet", "source": "text", "evidence": "卖家概括"}},
		"bonuses":        []any{map[string]any{"id": "b4", "status": "met", "source": "text", "evidence": "好"}},
		"checks":         []any{map[string]any{"key": "repaired", "status": "risk", "source": "text", "evidence": "修过"}, map[string]any{"key": "id_lock", "status": "ok", "source": "text", "evidence": "无锁"}, map[string]any{"key": "c5", "status": "risk", "source": "image", "evidence": "第1张图：亮斑"}, map[string]any{"key": "c6", "status": "suspect", "source": "text", "evidence": "概括"}, map[string]any{"key": "c7", "status": "risk", "source": nil, "evidence": nil}, map[string]any{"key": "seller_new", "status": "risk", "source": "text", "evidence": "新号"}, map[string]any{"key": "outer", "status": "ok"}},
		"seller_signals": []any{map[string]any{"key": "slow_ship", "evidence": "发货慢"}, map[string]any{"key": "dealer_words", "evidence": "商家"}, map[string]any{"key": "bad_key", "evidence": "发货慢"}}, "price_note": "急出换机",
	}
	got, w, err := ParseFacts(factBody(change), p, EvidenceOriginals{Title: "国行（未拆）２５６Ｇ", Description: "急出换机", RecentRateTexts: []string{"发货 慢！"}})
	if err != nil || len(w) < 7 {
		t.Fatal(got, w, err)
	}
	if got.Musts[0].Status != ReqMet || got.Musts[1].Status != ReqUnknown || got.Musts[2].Status != ReqUnknown || got.Bonuses[0].Status != ReqUnknown {
		t.Fatal(got)
	}
	if len(got.Checks) != 5 || got.Checks[0].Status != CheckSuspect || got.Checks[1].Status != CheckUnknown || got.Checks[2].Status != CheckRisk || *got.Checks[2].Source != "image" || got.Checks[4].Status != CheckSuspect {
		t.Fatal(got.Checks)
	}
	if len(got.SellerSignals) != 1 || got.PriceNote == nil || *got.PriceNote != "急出换机" || got.Specs[0].Value == nil || *got.Specs[0].Value != "256G" {
		t.Fatal(got)
	}
	// 证据不转小写。
	if evidencePresent("ABC", "abc") || evidencePresent("很好", "好") || !evidencePresent("ＡＢＣ　（原装）", "ABC原装") {
		t.Fatal("wrong evidence rules")
	}
}

func TestFactsExtraFindingsFilterBeforeLimit(t *testing.T) {
	extras := []any{map[string]any{"name": "空白证据", "source": "image", "evidence": "   "}, map[string]any{"name": "亮斑", "source": "text", "evidence": "亮斑"}, map[string]any{"name": "不成立", "source": "text", "evidence": "不在原文"}, map[string]any{"name": "", "source": "image", "evidence": "图"}, map[string]any{"name": "非法来源", "source": "other", "evidence": "图"}}
	for i := 0; i < 7; i++ {
		extras = append(extras, map[string]any{"name": strings.Repeat("新", i+1), "source": "image", "evidence": "第1张图：问题"})
	}
	got, _, err := ParseFacts(factBody(map[string]any{"extra_findings": extras}), Profile{Risks: []ProfileRisk{{ID: "c1", Name: " 亮斑 "}}}, EvidenceOriginals{Title: "有亮斑"})
	if err != nil || len(got.ExtraFindings) != 5 || got.ExtraFindings[0].Name != "新" || got.ExtraFindings[4].Name != "新新新新新" {
		t.Fatal(got, err)
	}
}

func TestFactsRevisionsComeOnlyFromRequestProfile(t *testing.T) {
	p := Profile{Musts: []Requirement{{ID: "m1", Rev: 4}, {ID: "m2"}}, Bonuses: []Requirement{{ID: "b3", Rev: 6}, {ID: "b4", Rev: 9}}, Risks: []ProfileRisk{{ID: "c5", Name: "维修", Rev: 7}, {ID: "c6", Name: "亮斑", Rev: 8}, {ID: "seller_new", Rev: 11}}}
	for _, forged := range []any{nil, 0, -1, 1, 999999999, json.RawMessage("1e1000"), "absent", "bogus", map[string]any{"bad": true}, []any{1}} {
		t.Run(fmt.Sprint(forged), func(t *testing.T) {
			body := factBody(map[string]any{
				"musts":   []any{map[string]any{"id": "m1", "status": "met", "source": "text", "evidence": "国行带盒", "rev": forged}},
				"bonuses": []any{map[string]any{"id": "b3", "status": "met", "source": "text", "evidence": "仍在保修", "rev": forged}},
				"checks":  []any{map[string]any{"key": "c5", "status": "risk", "source": "text", "evidence": "拆机维修", "rev": forged}},
			})
			if text, ok := forged.(string); ok && text == "absent" {
				var object map[string]any
				_ = json.Unmarshal(body, &object)
				for _, field := range []string{"musts", "bonuses", "checks"} {
					for _, row := range object[field].([]any) {
						delete(row.(map[string]any), "rev")
					}
				}
				body, _ = json.Marshal(object)
			}
			before := append([]byte{}, body...)
			f, w, err := ParseFacts(body, p, EvidenceOriginals{Title: "国行带盒，仍在保修，拆机维修"})
			if err != nil || len(w) != 0 || len(f.Musts) != 2 || f.Musts[0].Status != ReqMet || f.Musts[0].Rev != 4 || f.Musts[1].Rev != 1 || f.Musts[1].Status != ReqUnknown || len(f.Bonuses) != 2 || f.Bonuses[0].Status != ReqMet || f.Bonuses[0].Rev != 6 || f.Bonuses[1].Rev != 9 || len(f.Checks) != 2 || f.Checks[0].Status != CheckRisk || f.Checks[0].Rev != 7 || f.Checks[1].Rev != 8 {
				t.Fatalf("facts=%+v warnings=%+v err=%v", f, w, err)
			}
			if !bytes.Equal(body, before) || *f.Musts[0].Evidence != "国行带盒" || *f.Checks[0].Evidence != "拆机维修" || p.Musts[1].Rev != 0 {
				t.Fatal("原文或请求输入被改写")
			}
		})
	}
}

func TestGeneratedProfileDoesNotExposeInternalRevision(t *testing.T) {
	body := []byte(`{"summary":"找电脑","specs":[],"musts":[{"text":"国行","source":"user","rev":{"forged":true}}],"bonuses":[],"risks":[{"id":"id_lock","name":null,"hint":null,"question":null,"attitude":null,"source":"ai","rev":"bad"}],"allowed_kinds":[],"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[]}`)
	generated, _, err := ParseGeneratedProfile(body)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(generated)
	if err != nil || bytes.Contains(encoded, []byte(`"rev"`)) {
		t.Fatal(string(encoded), err)
	}
}
