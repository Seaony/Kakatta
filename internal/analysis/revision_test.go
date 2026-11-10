package analysis

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/Seaony/Kakatta/internal/limits"
)

func revisionFixture() Profile {
	return Profile{
		NextNo:  20,
		Specs:   []ProfileSpec{{Name: "容量", Options: []string{"128G", "256G"}, Values: []string{"256G"}, Source: SourceUser}},
		Musts:   []Requirement{{ID: "m1", Text: "国行", Source: SourceUser, Rev: 3}, {ID: "m2", Text: "带盒", Source: SourceUser}},
		Bonuses: []Requirement{{ID: "b3", Text: "在保", Source: SourceUser, Rev: 5}},
		Risks:   []ProfileRisk{{ID: "c4", Name: "亮斑", Hint: "屏幕有亮斑", Question: "有没有亮斑？", Attitude: Mind, Source: SourceUser, Rev: 7}, {ID: "id_lock", Attitude: Confirm, Source: SourceUser, Rev: 11}},
	}
}
func copyRevisionFixture(p Profile) Profile {
	b, _ := json.Marshal(p)
	var copied Profile
	_ = json.Unmarshal(b, &copied)
	return copied
}
func profileRevisions(p Profile) map[string]int64 {
	out := map[string]int64{}
	for _, r := range p.Musts {
		out[r.ID] = r.Rev
	}
	for _, r := range p.Bonuses {
		out[r.ID] = r.Rev
	}
	for _, r := range p.Risks {
		out[r.ID] = r.Rev
	}
	return out
}

func TestReviseProfileVersionsFollowJudgmentFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Profile)
		changed string
	}{
		{"must text", func(p *Profile) { p.Musts[0].Text = "日版" }, "m1"},
		{"bonus text", func(p *Profile) { p.Bonuses[0].Text = "未激活" }, "b3"},
		{"legacy missing revision text", func(p *Profile) { p.Musts[1].Text = "带发票" }, "m2"},
		{"custom name", func(p *Profile) { p.Risks[0].Name = "漏光" }, "c4"},
		{"custom hint", func(p *Profile) { p.Risks[0].Hint = "屏幕有白点" }, "c4"},
		{"raw whitespace", func(p *Profile) { p.Musts[0].Text = "国 行" }, "m1"},
		{"raw punctuation", func(p *Profile) { p.Risks[0].Hint += "。" }, "c4"},
		{"attitude", func(p *Profile) { p.Risks[0].Attitude = Reject }, ""},
		{"question", func(p *Profile) { p.Risks[0].Question = "可以拍一下吗？" }, ""},
		{"spec options and values", func(p *Profile) {
			p.Specs[0].Options = []string{"256G", "128G", "512G"}
			p.Specs[0].Values = []string{"128G"}
		}, ""},
		{"entry order", func(p *Profile) {
			p.Musts[0], p.Musts[1] = p.Musts[1], p.Musts[0]
			p.Risks[0], p.Risks[1] = p.Risks[1], p.Risks[0]
		}, ""},
		{"dictionary client text", func(p *Profile) {
			p.Risks[1].Name = "伪造名称"
			p.Risks[1].Hint = "伪造判断"
			p.Risks[1].Question = "伪造问句"
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := revisionFixture()
			proposed := copyRevisionFixture(old)
			tc.change(&proposed)
			beforeOld, _ := json.Marshal(old)
			beforeProposed, _ := json.Marshal(proposed)
			got, w, err := ReviseProfile(old, proposed)
			if err != nil || len(w) != 0 {
				t.Fatal(got, w, err)
			}
			expected := map[string]int64{"m1": 3, "m2": 1, "b3": 5, "c4": 7, "id_lock": 11}
			if tc.changed != "" {
				expected[tc.changed]++
			}
			if !reflect.DeepEqual(profileRevisions(got), expected) {
				t.Fatal(profileRevisions(got), expected)
			}
			afterOld, _ := json.Marshal(old)
			afterProposed, _ := json.Marshal(proposed)
			if string(beforeOld) != string(afterOld) || string(beforeProposed) != string(afterProposed) {
				t.Fatal("input mutated")
			}
			for _, r := range got.Risks {
				if r.ID == "id_lock" && (r.Name != "" || r.Hint != "" || r.Question != "") {
					t.Fatal("dictionary text was stored", r)
				}
			}
		})
	}
}

func TestReviseProfileIgnoresForgedVersions(t *testing.T) {
	for _, forged := range []int64{math.MinInt64, -1, 0, 1, 999, math.MaxInt64} {
		old := revisionFixture()
		proposed := copyRevisionFixture(old)
		for i := range proposed.Musts {
			proposed.Musts[i].Rev = forged
		}
		for i := range proposed.Bonuses {
			proposed.Bonuses[i].Rev = forged
		}
		for i := range proposed.Risks {
			proposed.Risks[i].Rev = forged
		}
		proposed.Bonuses = append(proposed.Bonuses, Requirement{Text: "带发票", Source: SourceUser, Rev: forged})
		before, _ := json.Marshal(proposed)
		got, _, err := ReviseProfile(old, proposed)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(proposed)
		if string(before) != string(after) {
			t.Fatal("forged input mutated")
		}
		expected := map[string]int64{"m1": 3, "m2": 1, "b3": 5, "b20": 1, "c4": 7, "id_lock": 11}
		if !reflect.DeepEqual(profileRevisions(got), expected) {
			t.Fatal(profileRevisions(got), expected)
		}
	}
}

func TestReviseProfilePreservesNextNoAndCopiesInputs(t *testing.T) {
	old := revisionFixture()
	proposed := copyRevisionFixture(old)
	proposed.NextNo = 0
	proposed.Musts = nil
	proposed.Bonuses = []Requirement{{Text: "新要求", Source: SourceUser}}
	got, _, err := ReviseProfile(old, proposed)
	if err != nil || got.Bonuses[0].ID != "b20" || got.NextNo != 21 {
		t.Fatal(got, err)
	}
	got.Specs[0].Options[0] = "changed"
	got.Risks[0].Name = "changed"
	if proposed.Specs[0].Options[0] != "128G" || old.Risks[0].Name != "亮斑" {
		t.Fatal("output aliases input")
	}
	old = Profile{NextNo: 1, Musts: []Requirement{{ID: "m9", Text: "旧要求", Source: SourceUser, Rev: 2}}}
	got, _, err = ReviseProfile(old, Profile{Bonuses: []Requirement{{Text: "新要求", Source: SourceUser}}})
	if err != nil || got.Bonuses[0].ID != "b10" {
		t.Fatal(got, err)
	}
}

func TestProfileRevisionsRejectNegativeAndProtectOverflow(t *testing.T) {
	for _, target := range []string{"old must", "old bonus", "old risk"} {
		old := revisionFixture()
		proposed := copyRevisionFixture(old)
		switch target {
		case "old must":
			old.Musts[0].Rev = -1
			proposed.Musts = nil
		case "old bonus":
			old.Bonuses[0].Rev = -1
			proposed.Bonuses = nil
		case "old risk":
			old.Risks[0].Rev = -1
			proposed.Risks = nil
		}
		_, _, err := ReviseProfile(old, proposed)
		assertInvalidRevision(t, err)
	}
	for _, target := range []string{"must", "bonus", "risk"} {
		old := revisionFixture()
		switch target {
		case "must":
			old.Musts[0].Rev = limits.MaxSafeInteger
		case "bonus":
			old.Bonuses[0].Rev = limits.MaxSafeInteger
		case "risk":
			old.Risks[0].Rev = limits.MaxSafeInteger
		}
		proposed := copyRevisionFixture(old)
		got, _, err := ReviseProfile(old, proposed)
		if err != nil {
			t.Fatal("unchanged max revision rejected", got, err)
		}
		switch target {
		case "must":
			proposed.Musts[0].Text = "日版"
		case "bonus":
			proposed.Bonuses[0].Text = "未激活"
		case "risk":
			proposed.Risks[0].Hint = "屏幕漏光"
		}
		_, _, err = ReviseProfile(old, proposed)
		assertInvalidRevision(t, err)
	}
	old := revisionFixture()
	old.Risks[1].Rev = limits.MaxSafeInteger
	proposed := copyRevisionFixture(old)
	proposed.Risks[1].Name = "伪造"
	proposed.Risks[1].Hint = "伪造"
	proposed.Risks[1].Rev = 1
	got, _, err := ReviseProfile(old, proposed)
	if err != nil || got.Risks[1].Rev != limits.MaxSafeInteger {
		t.Fatal(got, err)
	}
}

func TestProfileRevisionBoundaryUsesStoredVersionAndKeepsInputs(t *testing.T) {
	for _, which := range []string{"must", "bonus", "custom", "dictionary"} {
		t.Run(which, func(t *testing.T) {
			old := revisionFixture()
			var revision *int64
			var id string
			switch which {
			case "must":
				revision, id = &old.Musts[0].Rev, "m1"
			case "bonus":
				revision, id = &old.Bonuses[0].Rev, "b3"
			case "custom":
				revision, id = &old.Risks[0].Rev, "c4"
			default:
				revision, id = &old.Risks[1].Rev, "id_lock"
			}
			*revision = limits.MaxSafeInteger - 1
			proposed := copyRevisionFixture(old)
			switch which {
			case "must":
				proposed.Musts[0].Text, proposed.Musts[0].Rev = "日版", math.MaxInt64
			case "bonus":
				proposed.Bonuses[0].Text, proposed.Bonuses[0].Rev = "已激活", 1
			case "custom":
				proposed.Risks[0].Hint, proposed.Risks[0].Rev = "无压伤", -1
			default:
				proposed.Risks[1].Attitude, proposed.Risks[1].Rev = Ignore, math.MaxInt64
			}
			beforeOld, _ := json.Marshal(old)
			beforeProposed, _ := json.Marshal(proposed)
			got, _, err := ReviseProfile(old, proposed)
			want := limits.MaxSafeInteger
			if which == "dictionary" {
				want--
			}
			if err != nil || profileRevisions(got)[id] != want {
				t.Fatal("stored safe version not used", got, err)
			}
			*revision = limits.MaxSafeInteger + 1
			_, _, err = NormalizeProfile(old)
			assertInvalidRevision(t, err)
			_, _, err = ReviseProfile(old, proposed)
			assertInvalidRevision(t, err)
			*revision = limits.MaxSafeInteger - 1
			afterOld, _ := json.Marshal(old)
			afterProposed, _ := json.Marshal(proposed)
			if string(beforeOld) != string(afterOld) || string(beforeProposed) != string(afterProposed) {
				t.Fatal("revision validation mutated input")
			}
		})
	}
}

func TestReviseAndMergeRejectUnsafeOldWatermarksEvenAfterDeletion(t *testing.T) {
	for _, old := range []Profile{
		{NextNo: int(limits.MaxSafeInteger + 1)},
		{NextNo: int(limits.MinSafeInteger - 1)},
		{Musts: []Requirement{{ID: "m" + strconv.FormatInt(limits.MaxSafeInteger+1, 10), Text: "旧条件", Source: SourceUser}}},
		{Bonuses: []Requirement{{ID: "b" + strconv.FormatInt(limits.MaxSafeInteger, 10), Text: "旧条件", Source: SourceUser}}},
		{Risks: []ProfileRisk{{ID: "c92233720368547758080", Name: "旧问题", Source: SourceUser, Attitude: Mind}}},
	} {
		before, _ := json.Marshal(old)
		if _, _, err := ReviseProfile(old, Profile{}); err == nil {
			t.Errorf("deleted unsafe old counter accepted: %+v", old)
		}
		if _, _, _, err := MergeGeneratedProfile(old, GeneratedProfile{}, "", ""); err == nil {
			t.Errorf("generated merge accepted unsafe old counter: %+v", old)
		}
		after, _ := json.Marshal(old)
		if string(before) != string(after) {
			t.Fatal("old counter validation mutated input")
		}
	}
	for _, next := range []int{int(limits.MaxSafeInteger + 1), int(limits.MinSafeInteger - 1)} {
		if _, _, err := ReviseProfile(Profile{}, Profile{NextNo: next}); err == nil {
			t.Fatalf("unsafe proposed watermark accepted: %d", next)
		}
	}
	old := Profile{NextNo: 1, Musts: []Requirement{{ID: "m" + strconv.FormatInt(limits.MaxSafeInteger-1, 10), Text: "国行", Source: SourceUser, Rev: 1}}}
	got, _, err := ReviseProfile(old, old)
	if err != nil || int64(got.NextNo) != limits.MaxSafeInteger {
		t.Fatal("last valid old id rejected", got, err)
	}
	_, _, _, err = MergeGeneratedProfile(old, GeneratedProfile{Bonuses: []GeneratedRequirement{{Text: "在保", Source: SourceAI}}}, "", "")
	var invalid *Error
	if !errors.As(err, &invalid) || invalid.Message.Code != "analysis.profile_id_exhausted" {
		t.Fatal("generated merge advanced unsafe watermark", err)
	}
}
func assertInvalidRevision(t *testing.T, err error) {
	t.Helper()
	var structured *Error
	if !errors.As(err, &structured) || structured.Message.Code != "analysis.profile_invalid" || structured.Message.Args["field"] != "rev" {
		t.Fatalf("expected revision error: %v", err)
	}
}

func TestNormalizeImportedProfileInitializesMissingRevisions(t *testing.T) {
	p := revisionFixture()
	for i := range p.Musts {
		p.Musts[i].Rev = 0
	}
	for i := range p.Bonuses {
		p.Bonuses[i].Rev = 0
	}
	for i := range p.Risks {
		p.Risks[i].Rev = 0
	}
	got, _, err := NormalizeProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, rev := range profileRevisions(got) {
		if rev != 1 {
			t.Fatal(got)
		}
	}
	p.Musts[0].Rev = -1
	_, _, err = NormalizeProfile(p)
	assertInvalidRevision(t, err)
}

func TestGeneratedMergeUsesServerRevisions(t *testing.T) {
	old := revisionFixture()
	generated := GeneratedProfile{Musts: []GeneratedRequirement{{Text: "国 行", Source: SourceUser}}, Bonuses: []GeneratedRequirement{{Text: "在保", Source: SourceAI}}, Risks: []GeneratedRisk{{ID: ptr("id_lock"), Attitude: ptr(Ignore), Source: SourceAI}, {Name: ptr("亮斑"), Hint: ptr("屏幕有白点"), Source: SourceAI}}}
	before, _ := json.Marshal(old)
	got, _, _, err := MergeGeneratedProfile(old, generated, "国行电脑", "")
	if err != nil {
		t.Fatal(err)
	}
	revs := profileRevisions(got)
	if revs["m1"] != 4 || revs["b3"] != 5 || revs["c4"] != 8 || revs["id_lock"] != 11 || revs["off_platform"] != 1 {
		t.Fatal(got)
	}
	after, _ := json.Marshal(old)
	if string(before) != string(after) {
		t.Fatal("old profile mutated")
	}
	old.Risks[0].Edited = true
	got, _, _, err = MergeGeneratedProfile(old, generated, "国行电脑", "")
	if err != nil || got.Risks[1].ID != "c4" || got.Risks[1].Rev != 7 || got.Risks[1].Hint != "屏幕有亮斑" {
		t.Fatal(got, err)
	}
}

func TestReviseProfileCannotResurrectDeletedFixedIDs(t *testing.T) {
	old := revisionFixture()
	for _, which := range []string{"must", "bonus", "risk"} {
		proposed := copyRevisionFixture(old)
		switch which {
		case "must":
			proposed.Musts = append(proposed.Musts, Requirement{ID: "m8", Text: "新条件", Source: SourceUser, Rev: 1})
		case "bonus":
			proposed.Bonuses = append(proposed.Bonuses, Requirement{ID: "b8", Text: "新条件", Source: SourceUser, Rev: 1})
		case "risk":
			proposed.Risks = append(proposed.Risks, ProfileRisk{ID: "c8", Name: "新问题", Source: SourceUser, Attitude: Mind, Rev: 1})
		}
		_, _, err := ReviseProfile(old, proposed)
		var structured *Error
		if !errors.As(err, &structured) || structured.Message.Code != "analysis.profile_invalid" || structured.Message.Args["field"] != "id" {
			t.Fatalf("%s: deleted id accepted: %v", which, err)
		}
	}
	legacy := Profile{NextNo: 1, Musts: []Requirement{{ID: "m9", Text: "旧条件", Source: SourceUser, Rev: 1}}}
	if _, _, err := ReviseProfile(legacy, Profile{Bonuses: []Requirement{{ID: "b8", Text: "新条件", Source: SourceUser}}}); err == nil {
		t.Fatal("largest old ID did not protect the watermark")
	}
	oldBonusFact := RequirementFact{ID: "b2", Status: ReqMet, Rev: 1}
	proposed := copyRevisionFixture(old)
	proposed.Bonuses = []Requirement{{ID: "b2", Text: "新条件", Source: SourceUser, Rev: 1}}
	if _, _, err := ReviseProfile(old, proposed); err == nil {
		t.Fatal("deleted b2 reused")
	}
	proposed.Bonuses[0].ID = ""
	got, _, err := ReviseProfile(old, proposed)
	if err != nil || got.Bonuses[0].ID != "b20" || got.Bonuses[0].Rev != 1 || oldBonusFact.ID == got.Bonuses[0].ID {
		t.Fatal(got, err)
	}
	// 水位以上的未知显式编号留给保存服务最终校验，不在纯包扩大限制。
	proposed.Bonuses[0].ID = "b25"
	proposed.NextNo = 50
	got, _, err = ReviseProfile(old, proposed)
	if err != nil || got.Bonuses[0].ID != "b25" || got.Bonuses[0].Rev != 1 || got.NextNo != 50 {
		t.Fatal(got, err)
	}
}

func TestNormalizeProfileKeepsImportedPositiveVersions(t *testing.T) {
	p := revisionFixture()
	got, _, err := NormalizeProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int64{"m1": 3, "m2": 1, "b3": 5, "c4": 7, "id_lock": 11}
	if !reflect.DeepEqual(profileRevisions(got), expected) {
		t.Fatal(got)
	}
	for _, set := range []string{"bonus", "risk"} {
		bad := revisionFixture()
		if set == "bonus" {
			bad.Bonuses[0].Rev = -1
		} else {
			bad.Risks[0].Rev = -1
		}
		_, _, err := NormalizeProfile(bad)
		assertInvalidRevision(t, err)
	}
}
