package analysis

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestGeneratedProfileParsing(t *testing.T) {
	body := `{"summary":"会帮你找：电脑","specs":[],"musts":[],"bonuses":[],"risks":[],"allowed_kinds":[],"budget_min":1.25,"budget_max":null,"keyword":null,"exclude_words":[]}`
	got, _, err := ParseGeneratedProfile([]byte("<think>内部</think>```json\n" + body + "\n```"))
	if err != nil || got.BudgetMin == nil || *got.BudgetMin != 1.25 {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"<think>未闭合", "<think>只有思考</think>", `{"specs":[],"risks":[]}`, `{"summary":"x","specs":[],"musts":[],"bonuses":[],"risks":[],"allowed_kinds":[],"budget_min":"1","budget_max":null,"keyword":null,"exclude_words":[]}`} {
		if _, _, err := ParseGeneratedProfile([]byte(bad)); err == nil {
			t.Error("accepted", bad)
		}
	}
}

func TestProfileMergeProtectsEditedAndIDs(t *testing.T) {
	old := Profile{NextNo: 12, Specs: []ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"256G"}, Source: SourceUser, Edited: true}, {Name: "颜色", Options: []string{"黑"}, Values: []string{}, Source: SourceUser, Edited: true}}, Musts: []Requirement{{ID: "m1", Text: "国行", Source: SourceUser, Edited: true, Rev: 1}, {ID: "m7", Text: "旧条件", Source: SourceUser}}, Bonuses: []Requirement{{ID: "b8", Text: "电池健康", Source: SourceAI}}, Risks: []ProfileRisk{{ID: "c9", Name: "亮斑", Attitude: Reject, Source: SourceUser, Edited: true}, {ID: "id_lock", Attitude: Ignore, Source: SourceUser, Edited: true}}}
	before, _ := json.Marshal(old)
	gen := GeneratedProfile{Specs: []GeneratedSpec{{Name: "存储", Options: []string{"128G"}, Values: []string{"128G"}, Source: SourceAI}}, Musts: []GeneratedRequirement{{Text: "国行", Source: SourceAI}, {Text: "全新未拆", Source: SourceUser}, {Text: "带盒", Source: SourceAI}}, Bonuses: []GeneratedRequirement{{Text: "电池健康", Source: SourceAI}}, Risks: []GeneratedRisk{{ID: ptr("id_lock"), Attitude: ptr(Confirm), Source: SourceAI}, {Name: ptr("亮斑"), Attitude: ptr(Confirm), Source: SourceAI}, {Name: ptr("其他问题"), Attitude: ptr(Reject), Source: SourceUser}}}
	p, _, warnings, err := MergeGeneratedProfile(old, gen, "电脑", "想要全新未拆")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) < 2 {
		t.Fatal(warnings)
	}
	if !reflect.DeepEqual(p.Specs, old.Specs) || !reflect.DeepEqual(p.Musts[0], old.Musts[0]) {
		t.Fatal(p)
	}
	if len(p.Musts) != 2 || p.Musts[1].Text != "全新未拆" || p.Musts[1].ID != "m12" {
		t.Fatal(p.Musts)
	}
	if len(p.Bonuses) != 2 || p.Bonuses[0].Text != "带盒" || p.Bonuses[0].ID != "b13" || p.Bonuses[1].ID != "b8" {
		t.Fatal(p.Bonuses)
	}
	if p.Risks[0].Attitude != Ignore || p.Risks[1].ID != "c9" || p.Risks[1].Attitude != Reject || p.Risks[2].ID != "c14" || p.Risks[2].Attitude != Mind {
		t.Fatal(p.Risks)
	}
	if p.NextNo != 15 {
		t.Fatal(p.NextNo)
	}
	after, _ := json.Marshal(old)
	if string(before) != string(after) {
		t.Fatal("mutated old")
	}
	p.Specs[0].Options[0] = "changed"
	if old.Specs[0].Options[0] != "128G" {
		t.Fatal("aliased spec")
	}
}

func TestProfileMustProvenanceAndDictionaryResolution(t *testing.T) {
	gen := GeneratedProfile{Musts: []GeneratedRequirement{{"国行", SourceUser}, {"９５新", SourceUser}, {"保证正品", SourceUser}, {"自用", SourceAI}}, Risks: []GeneratedRisk{
		{ID: ptr("id_lock"), Name: ptr("假的名称"), Attitude: ptr(Mind), Source: SourceUser},
		{Name: ptr("ＩＤ　锁"), Attitude: ptr(Confirm), Source: SourceAI},
		{ID: ptr("invalid"), Name: ptr("屏幕压伤"), Attitude: ptr(Confirm), Source: SourceAI},
		{Name: ptr(" 屏幕压伤 "), Attitude: ptr(Ignore), Source: SourceAI}, {Name: ptr(" "), Source: SourceAI},
	}}
	p, _, _, err := MergeGeneratedProfile(Profile{}, gen, "国行电脑", "95 新")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Musts) != 2 || len(p.Bonuses) != 2 || p.Bonuses[0].Source != SourceAI {
		t.Fatal(p)
	}
	if p.Risks[0].ID != "id_lock" || p.Risks[0].Name != "" || p.Risks[0].Attitude != Mind || p.Risks[1].Attitude != Mind || len(p.Risks) != 14 {
		t.Fatal(p.Risks)
	}
	if r := ResolveRisks(p); r[0].Name != "ID 锁" {
		t.Fatal(r)
	}
	d := DefaultProfile()
	if len(d.Risks) != 12 || d.Risks[0].ID != "photo_mismatch" || d.Risks[11].ID != "seller_partial_refund" {
		t.Fatal(d)
	}
}

func TestProfileMergeKeepsEditedAcrossCollectionsAndNeverRecycles(t *testing.T) {
	old := Profile{NextNo: 30, Musts: []Requirement{{ID: "m1", Text: "国行", Source: SourceUser, Edited: true, Rev: 1}}, Bonuses: []Requirement{{ID: "b2", Text: "电池健康", Source: SourceUser, Edited: true}}, Risks: []ProfileRisk{{ID: "c20", Name: "亮斑", Source: SourceUser, Attitude: Reject, Edited: true}}}
	gen := GeneratedProfile{Musts: []GeneratedRequirement{{"电池健康", SourceUser}}, Bonuses: []GeneratedRequirement{{"国行", SourceAI}}}
	got, _, _, err := MergeGeneratedProfile(old, gen, "电池健康", "国行")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Musts) != 1 || got.Musts[0].ID != "m1" || len(got.Bonuses) != 1 || got.Bonuses[0].ID != "b2" {
		t.Fatal(got)
	}
	if got.NextNo != 30 {
		t.Fatal(got.NextNo)
	}
	old.NextNo = 1
	got, _, _, err = MergeGeneratedProfile(old, GeneratedProfile{Bonuses: []GeneratedRequirement{{"新条件", SourceAI}}}, "", "")
	if err != nil || got.Bonuses[0].ID != "b21" {
		t.Fatal(got, err)
	}
	old.NextNo = math.MaxInt
	_, _, _, err = MergeGeneratedProfile(old, GeneratedProfile{Bonuses: []GeneratedRequirement{{"新条件", SourceAI}}}, "", "")
	if err == nil {
		t.Fatal("ID overflow accepted")
	}
}

func TestNormalizeProfileAllocatesAndPreservesWatermark(t *testing.T) {
	p := Profile{NextNo: 20, Musts: []Requirement{{ID: "m2", Text: "AI加的条件", Source: SourceAI}, {Text: "国行", Source: SourceUser, Edited: true}}, Bonuses: []Requirement{{ID: "b9", Text: "在保", Source: SourceAI}}, Risks: []ProfileRisk{{ID: "id_lock", Name: "不应存储", Hint: "伪造", Question: "伪造", Attitude: Ignore, Source: SourceUser, Edited: true}, {Name: "亮斑", Attitude: Reject, Source: SourceUser, Edited: true}}}
	got, w, err := NormalizeProfile(p)
	if err != nil || len(w) != 1 {
		t.Fatal(got, w, err)
	}
	if len(got.Musts) != 1 || got.Musts[0].ID != "m21" || got.Bonuses[0].ID != "b20" || got.Bonuses[1].ID != "b9" || got.Risks[1].ID != "c22" || got.NextNo != 23 {
		t.Fatal(got)
	}
	if len(got.Risks) != 2 || got.Risks[0].Name != "" || got.Risks[0].Hint != "" || got.Risks[0].Question != "" || got.Risks[1].Attitude != Reject {
		t.Fatal(got.Risks)
	}
	if p.Musts[0].ID != "m2" || p.Risks[0].Name != "不应存储" {
		t.Fatal("input mutated")
	}
	// 空ID的AI must只消耗b号，旧的删除水位不会重用。
	got, _, err = NormalizeProfile(Profile{NextNo: 50, Musts: []Requirement{{Text: "在保", Source: SourceAI}}})
	if err != nil || got.Bonuses[0].ID != "b50" || got.NextNo != 51 {
		t.Fatal(got, err)
	}
	got, _, err = NormalizeProfile(Profile{NextNo: 1, Bonuses: []Requirement{{ID: "b30", Text: "在保", Source: SourceUser}, {Text: "带盒", Source: SourceUser}}})
	if err != nil || got.Bonuses[1].ID != "b31" {
		t.Fatal(got, err)
	}
}

func TestNormalizeProfileRejectsInvalidSavedShape(t *testing.T) {
	for _, p := range []Profile{
		{Musts: []Requirement{{ID: "b1", Text: "国行", Source: SourceUser}}},
		{Bonuses: []Requirement{{ID: "b1", Text: "国行", Source: SourceUser}, {ID: "b1", Text: "在保", Source: SourceUser}}},
		{Risks: []ProfileRisk{{ID: "c1", Name: " ", Attitude: Mind, Source: SourceUser}}},
		{Risks: []ProfileRisk{{ID: "unknown", Name: "新风险", Attitude: Mind, Source: SourceUser}}},
		{Risks: []ProfileRisk{{ID: "c1", Name: "新风险", Attitude: "bad", Source: SourceUser}}},
		{Specs: []ProfileSpec{{Name: "型号", Source: SourceSystem}}},
		{Specs: []ProfileSpec{{Name: "型号", Source: SourceUser, Options: []string{"A"}, Values: []string{"B"}}}},
		{Specs: []ProfileSpec{{Name: "型号", Source: SourceUser}, {Name: "型号", Source: SourceUser}}},
		{NextNo: math.MaxInt, Bonuses: []Requirement{{Text: "新条件", Source: SourceUser}}},
	} {
		if _, _, err := NormalizeProfile(p); err == nil {
			t.Error("accepted", p)
		}
	}
	// 用户可以设置超过4项规格，4项只约束生成提示词。
	p := Profile{Specs: []ProfileSpec{}}
	for _, name := range []string{"A", "B", "C", "D", "E"} {
		p.Specs = append(p.Specs, ProfileSpec{Name: name, Source: SourceUser})
	}
	if _, _, err := NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedProfileRequiresRiskProtocolFields(t *testing.T) {
	body := `{"summary":"x","specs":[],"musts":[],"bonuses":[],"risks":[{"source":"ai"}],"allowed_kinds":[],"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[]}`
	if _, _, err := ParseGeneratedProfile([]byte(body)); err == nil {
		t.Fatal("incomplete risk shape accepted")
	}
}

func TestGeneratedProfileRejectsNullArrayMembersAndInvalidMustSource(t *testing.T) {
	for _, body := range []string{
		`{"summary":"x","specs":[{"name":"型号","options":[null],"values":[],"source":"ai"}],"musts":[],"bonuses":[],"risks":[],"allowed_kinds":[],"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[]}`,
		`{"summary":"x","specs":[],"musts":[],"bonuses":[],"risks":[],"allowed_kinds":[],"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[null]}`,
	} {
		if _, _, err := ParseGeneratedProfile([]byte(body)); err == nil {
			t.Fatal("accepted", body)
		}
	}
	if _, _, _, err := MergeGeneratedProfile(Profile{}, GeneratedProfile{Musts: []GeneratedRequirement{{Text: "国行", Source: "invalid"}}}, "", ""); err == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestNormalizeCustomAIRisksCannotBecomeStrictWithoutUserEdit(t *testing.T) {
	for _, tc := range []struct {
		source ProfileSource
		edited bool
		want   Attitude
	}{
		{SourceAI, false, Mind}, {SourceAI, true, Confirm}, {SourceUser, false, Confirm}, {SourceUser, true, Confirm},
	} {
		p := Profile{Risks: []ProfileRisk{{ID: "c1", Name: "亮斑", Source: tc.source, Edited: tc.edited, Attitude: Confirm}}, NextNo: 2}
		got, warnings, err := NormalizeProfile(p)
		if err != nil || got.Risks[0].Attitude != tc.want {
			t.Fatalf("source=%s edited=%t: %+v %v", tc.source, tc.edited, got, err)
		}
		if tc.want == Mind && len(warnings) != 1 {
			t.Fatalf("降级应提示用户：%v", warnings)
		}
		if p.Risks[0].Attitude != Confirm {
			t.Fatal("原输入被改写")
		}
	}
	p := Profile{Risks: []ProfileRisk{{ID: "id_lock", Source: SourceAI, Attitude: Confirm}}, NextNo: 1}
	got, _, err := NormalizeProfile(p)
	if err != nil || got.Risks[0].Attitude != Confirm {
		t.Fatal("字典风险不受自定义项加严限制", got, err)
	}
}
