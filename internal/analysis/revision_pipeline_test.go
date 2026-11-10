package analysis

import (
	"encoding/json"
	"math"
	"testing"
)

func TestProfileRevisionInvalidatesOldEvidenceWithoutDeletingIt(t *testing.T) {
	old := Profile{NextNo: 4,
		Musts:   []Requirement{{ID: "m1", Text: "国行", Source: SourceUser, Edited: true, Rev: 7}},
		Bonuses: []Requirement{{ID: "b2", Text: "配件齐全", Source: SourceUser, Rev: 3}},
		Risks:   []ProfileRisk{{ID: "c3", Name: "亮斑", Hint: "屏幕亮斑", Attitude: Confirm, Source: SourceUser, Edited: true, Rev: 4}},
	}
	body := []byte(`{"musts":[{"id":"m1","status":"unmet","source":"text","evidence":"不是国行","rev":{"forged":true}}],"bonuses":[{"id":"b2","status":"met","source":"text","evidence":"配件齐全","rev":999}],"checks":[{"key":"c3","status":"ok","source":"text","evidence":"屏幕无亮斑","rev":"forged"}],"adjust":{"value":0,"reason":""},"summary":"旧需求的结论"}`)
	facts, _, err := ParseFacts(body, old, EvidenceOriginals{Description: "不是国行，配件齐全，屏幕无亮斑"})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Musts[0].Rev != 7 || facts.Bonuses[0].Rev != 3 || facts.Checks[0].Rev != 4 {
		t.Fatalf("模型伪造版本进入事实：%+v", facts)
	}
	oldJSON, _ := json.Marshal(old)
	factsJSON, _ := json.Marshal(facts)
	var proposed Profile
	if err = json.Unmarshal(oldJSON, &proposed); err != nil {
		t.Fatal(err)
	}
	proposed.Musts[0].Text = "日版"
	proposed.Musts[0].Rev = 1
	proposed.Bonuses[0].Text = "原盒票齐"
	proposed.Bonuses[0].Rev = math.MaxInt64
	proposed.Risks[0].Name = "鼓包"
	proposed.Risks[0].Hint = "电池鼓包"
	proposed.Risks[0].Rev = 1
	current, _, err := ReviseProfile(old, proposed)
	if err != nil {
		t.Fatal(err)
	}
	if current.Musts[0].Rev != 8 || current.Bonuses[0].Rev != 4 || current.Risks[0].Rev != 5 {
		t.Fatalf("没有按服务端旧版本递增：%+v", current)
	}
	overrides := UserOverrides{Risks: map[string]RiskOverride{"c3": {Status: CheckOK, Rev: 4}}}
	overrideJSON, _ := json.Marshal(overrides)
	in := ScoreInput{Profile: current, Facts: facts, Item: ResolveSpecs(current, nil, nil, SpecDetail), Price: ptr(int64(6500)), Fair: &FairPrice{Cents: ptr(int64(10000)), Sample: 6}, Overrides: overrides}
	got := Score(in)
	if got.Decision != DecisionScored || got.Excluded || got.Score == nil || *got.Score != 89 || len(got.Breakdown.Items) != 0 || got.Checks[0].Status != CheckUnknown {
		t.Fatalf("旧事实/覆盖冒充新条件：%+v", got)
	}
	if len(got.Questions) != 1 || got.Questions[0].Key != "c3" {
		t.Fatalf("新版本未确认项未追问：%+v", got.Questions)
	}
	oldAfter, _ := json.Marshal(old)
	factsAfter, _ := json.Marshal(facts)
	overrideAfter, _ := json.Marshal(overrides)
	if string(oldAfter) != string(oldJSON) || string(factsAfter) != string(factsJSON) || string(overrideAfter) != string(overrideJSON) {
		t.Fatal("版本失效删除或改写了旧内容")
	}

	freshBody := []byte(`{"musts":[{"id":"m1","status":"met","source":"text","evidence":"日版"}],"bonuses":[{"id":"b2","status":"met","source":"text","evidence":"原盒票齐"}],"checks":[{"key":"c3","status":"risk","source":"text","evidence":"电池鼓包"}],"adjust":{"value":0,"reason":""},"summary":"当前需求的结论"}`)
	fresh, _, err := ParseFacts(freshBody, current, EvidenceOriginals{Description: "日版、原盒票齐，电池鼓包"})
	if err != nil {
		t.Fatal(err)
	}
	in.Facts = fresh
	got = Score(in)
	if got.Score == nil || *got.Score != 39 || len(got.Breakdown.Gates) == 0 {
		t.Fatalf("旧ok覆盖消掉新版本文字风险：%+v", got)
	}
	in.Overrides = UserOverrides{Risks: map[string]RiskOverride{"c3": {Status: CheckOK, Rev: 5}}}
	got = Score(in)
	if got.Score == nil || *got.Score != 96 || len(got.Breakdown.Gates) != 0 || got.Checks[0].Source != EvidenceUser {
		t.Fatalf("当前版本手动结论未生效：%+v", got)
	}
}

func TestModelProfileAndTitleWireKeepRevisionsInternal(t *testing.T) {
	objects := []any{
		GeneratedProfile{Musts: []GeneratedRequirement{{Text: "国行", Source: SourceUser}}, Risks: []GeneratedRisk{{ID: ptr("id_lock")}}},
		TitleScreen{Items: []TitleItem{{No: 1, Kind: KindSale, Verdict: "pass", Specs: []SpecValue{}}}},
	}
	for _, obj := range objects {
		b, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err = json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		var inspect func(any)
		inspect = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				for key, item := range x {
					if key == "rev" {
						t.Fatalf("内部版本进入模型协议：%s", b)
					}
					inspect(item)
				}
			case []any:
				for _, item := range x {
					inspect(item)
				}
			}
		}
		inspect(value)
	}
}
