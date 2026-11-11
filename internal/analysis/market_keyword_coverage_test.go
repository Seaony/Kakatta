package analysis

import (
	"fmt"
	"reflect"
	"testing"
)

func TestJudgeKeyword命中列表和原因保留归一化顺序(t *testing.T) {
	title, description := "Ｑ１ 相机", "支持 iPad\t MINI，附收音机，另有 Q1"
	matches := []string{"收音机", " Ｑ１ ", "iPad\n MINI", "q1", "未出现"}
	want := []string{"收音机", "q1", "ipad mini"}
	if got := MatchingKeywords(title+"\n"+description, matches); !reflect.DeepEqual(got, want) {
		t.Fatalf("完整命中列表和顺序不符：%v，期望 %v", got, want)
	}
	got := JudgeKeyword(title, description, matches, nil)
	if got.Rejected || got.RejectKind != "" || !reflect.DeepEqual(got.MatchedKeywords, want) {
		t.Fatalf("成功匹配丢失命中词或被拒绝：%+v", got)
	}
	if got.Reason == nil || got.Reason.Code != "analysis.keyword_matched" || !reflect.DeepEqual(got.Reason.Args, map[string]any{"count": 3, "words": "收音机、q1、ipad mini"}) {
		t.Fatalf("命中原因没有保留完整数量和词序：%+v", got.Reason)
	}
}

func TestJudgeKeyword排除优先且无匹配词给出明确原因(t *testing.T) {
	got := JudgeKeyword("Ｑ１ 有锁", "附收音机，需要维修", []string{"q1", "收音机"}, []string{" 维修 ", "有锁"})
	if !got.Rejected || got.RejectKind != RejectExcluded || !reflect.DeepEqual(got.MatchedKeywords, []string{}) {
		t.Fatalf("排除词没有优先于成功匹配：%+v", got)
	}
	if got.Reason == nil || got.Reason.Code != "analysis.keyword_excluded" || !reflect.DeepEqual(got.Reason.Args, map[string]any{"word": "维修"}) {
		t.Fatalf("排除原因没有采用规则中的第一个命中词：%+v", got.Reason)
	}
	for _, excludes := range [][]string{nil, {"有锁"}} {
		got = JudgeKeyword("普通商品", "正常出售", nil, excludes)
		if got.Rejected || got.RejectKind != "" || !reflect.DeepEqual(got.MatchedKeywords, []string{}) {
			t.Fatalf("没填匹配词且未被排除时没有通过：%+v", got)
		}
		if got.Reason == nil || got.Reason.Code != "analysis.keyword_no_exclusion" || !reflect.DeepEqual(got.Reason.Args, map[string]any{}) {
			t.Fatalf("无排除命中的原因不准确：%+v", got.Reason)
		}
	}
}

func TestMarket唯一规格不能借用其他组的样本(t *testing.T) {
	in := marketInput()
	in.Profile.Specs = []ProfileSpec{{Name: "型号", Options: []string{"A", "B", "C"}}}
	want := map[string][]string{
		"A": {"A-0", "A-1"},
		"B": {"B-0", "B-1", "B-2"},
		"C": {"C-0", "C-1", "C-2"},
	}
	for value, ids := range want {
		for _, id := range ids {
			row := valuationObservation(id, "2026-11-10", 100)
			row.Specs = []SpecValue{{Name: "型号", Value: ptr(value)}}
			in.Observations = append(in.Observations, row)
		}
	}
	got := CalculateMarket(in)
	if len(got.Groups) != 3 {
		t.Fatalf("单规格组被合并或遗漏：%+v", got)
	}
	actual := map[string][]string{}
	for _, group := range got.Groups {
		ids, exists := want[group.ConfigKey]
		if !exists || group.Fair.Cents != nil || group.Fair.DroppedSpec != nil || group.Fair.Days != 30 || group.Fair.Sample != len(ids) {
			t.Fatalf("唯一规格被放宽或样本数被其他组填满：%+v", group)
		}
		if !reflect.DeepEqual(group.Fair.ItemIDs, ids) {
			t.Fatalf("合理价依据混入其他配置：%+v", group.Fair)
		}
		for _, sample := range group.Samples {
			actual[group.ConfigKey] = append(actual[group.ConfigKey], sample.ItemID)
		}
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("原组样本没有完整保留：%v，期望 %v", actual, want)
	}
}

func TestMarket十四日涨跌必须使用样本足够的原始首尾(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, last int
		trend       bool
	}{
		{"最早两件不能跳到下一天", 2, 3, false},
		{"最近两件不能退到前一天", 3, 2, false},
		{"首尾各三件显示涨跌", 3, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := marketInput()
			for _, day := range []struct {
				date  string
				count int
				price int64
			}{
				{"2026-10-28", tc.first, 100},
				{"2026-10-29", 3, 125},
				{"2026-11-09", 3, 175},
				{"2026-11-10", tc.last, 200},
			} {
				for i := 0; i < day.count; i++ {
					in.Observations = append(in.Observations, valuationObservation(fmt.Sprintf("%s-%d", day.date, i), day.date, day.price))
				}
			}
			group := onlyMarket(t, in)
			if len(group.Daily) != 14 || group.Daily[0].Day != "2026-10-28" || group.Daily[0].Sample != tc.first || group.Daily[1].Sample != 3 || group.Daily[12].Sample != 3 || group.Daily[13].Day != "2026-11-10" || group.Daily[13].Sample != tc.last {
				t.Fatalf("原始首尾或中间合格日期被丢弃：%+v", group.Daily)
			}
			if !tc.trend {
				if group.Trend != nil {
					t.Fatalf("首尾有一天不足三件仍显示涨跌：%+v", group.Trend)
				}
				return
			}
			if group.Trend == nil || group.Trend.FirstDay != "2026-10-28" || group.Trend.LastDay != "2026-11-10" || group.Trend.FirstMedian != 100 || group.Trend.LastMedian != 200 || group.Trend.Change != 100 || group.Trend.Percent == nil || *group.Trend.Percent != 1 {
				t.Fatalf("首尾各三件没有按原日期计算涨跌：%+v", group.Trend)
			}
		})
	}
}
