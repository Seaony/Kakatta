package notification

import (
	"reflect"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
)

func TestFormat单条只有捡漏设为紧急(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grade  *analysis.Grade
		urgent bool
	}{
		{"捡漏", formatPtr(analysis.GradeSteal), true},
		{"划算", formatPtr(analysis.GradeGood), false},
		{"价格合理", formatPtr(analysis.GradeFair), false},
		{"偏贵", formatPtr(analysis.GradePricey), false},
		{"不建议", formatPtr(analysis.GradeAvoid), false},
		{"无法比价", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := formatCandidate(1, intentRecommended, formatPtr(int64(91)))
			value.Finding.Grade = tc.grade
			message, err := formatGroup(formatTask(analysis.ModeAI), messageGroup{Members: []candidate{value}}, notify.Bark, formatOpts(locales.ZhCN))
			if err != nil || message.Urgent != tc.urgent {
				t.Fatalf("单条通知的紧急标志不符：%+v %v，期望 %t", message, err, tc.urgent)
			}
		})
	}
}

func TestGroupMessages超出上限只单发前若干条再合并其余(t *testing.T) {
	values := []candidate{}
	for id := int64(6); id >= 1; id-- {
		values = append(values, formatCandidate(id, intentRecommended, formatPtr(int64(84))))
	}
	for _, tc := range []struct {
		name    string
		limit   int64
		ids     [][]int64
		summary bool
	}{
		{"上限一条时全部合并", 1, [][]int64{{1, 2, 3, 4, 5, 6}}, true},
		{"上限两条时单发一条", 2, [][]int64{{1}, {2, 3, 4, 5, 6}}, true},
		{"默认上限单发四条", 5, [][]int64{{1}, {2}, {3}, {4}, {5, 6}}, true},
		{"刚好达到上限不合并", 6, [][]int64{{1}, {2}, {3}, {4}, {5}, {6}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := groupMessages(formatTask(analysis.ModeAI), values, tc.limit)
			if err != nil || len(groups) != len(tc.ids) {
				t.Fatalf("单条与合并消息数量不符：%+v %v", groups, err)
			}
			ids := make([][]int64, len(groups))
			for index, group := range groups {
				if want := tc.summary && index == len(groups)-1; group.Summary != want {
					t.Fatalf("第%d条消息的合并标志为%t，期望%t", index+1, group.Summary, want)
				}
				for _, member := range group.Members {
					ids[index] = append(ids[index], member.Finding.ID)
				}
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("前几条单发或剩余合并的成员不符：%v，期望%v", ids, tc.ids)
			}
		})
	}
}

func TestGroupMessages关键词推荐按已知发布日期从新到旧(t *testing.T) {
	values := []candidate{}
	for _, row := range []struct {
		id        int64
		published *int64
	}{
		{2, formatPtr(int64(100))}, {6, nil}, {4, formatPtr(int64(200))},
		{1, nil}, {5, formatPtr(int64(300))}, {3, formatPtr(int64(300))},
	} {
		value := formatCandidate(row.id, intentRecommended, nil)
		value.Listing.PublishedAt = row.published
		values = append(values, value)
	}
	groups, err := groupMessages(formatTask(analysis.ModeKeyword), values, 6)
	if err != nil || len(groups) != 6 {
		t.Fatalf("关键词推荐分组失败：%+v %v", groups, err)
	}
	ids := make([]int64, len(groups))
	for index, group := range groups {
		if group.Summary || len(group.Members) != 1 {
			t.Fatalf("未超过上限的推荐被合并：%+v", group)
		}
		ids[index] = group.Members[0].Finding.ID
	}
	if want := []int64{3, 5, 4, 2, 1, 6}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("发布日期未倒序、同日或未知日期顺序不稳定：%v，期望%v", ids, want)
	}
}

func TestFormat摘要每行保留档位分数和商品标题(t *testing.T) {
	members := []candidate{
		formatCandidate(1, intentRecommended, formatPtr(int64(91))),
		formatCandidate(2, intentRecommended, formatPtr(int64(84))),
		formatCandidate(3, intentRecommended, formatPtr(int64(72))),
		formatCandidate(4, intentRecommended, nil),
	}
	members[0].Finding.Grade, members[0].Listing.Title = formatPtr(analysis.GradeSteal), "平板甲"
	members[1].Listing.Title = "平板\n乙"
	members[2].Finding.Grade, members[2].Listing.Title = formatPtr(analysis.GradeFair), "平板丙"
	members[3].Finding.Grade, members[3].Listing.Title = nil, "无法比价丁"
	for _, tc := range []struct {
		language locales.Language
		body     string
	}{
		{locales.ZhCN, "捡漏 91 · 平板甲\n划算 84 · 平板 乙\n价格合理 72 · 平板丙\n无法比价丁"},
		{locales.En, "A steal 91 · 平板甲\nGood deal 84 · 平板 乙\nFair price 72 · 平板丙\n无法比价丁"},
	} {
		t.Run(string(tc.language), func(t *testing.T) {
			message, err := formatGroup(formatTask(analysis.ModeAI), messageGroup{Members: members, Summary: true}, notify.Bark, formatOpts(tc.language))
			if err != nil || message.Body != tc.body {
				t.Fatalf("摘要没有逐行保留独立的档位、分数和标题：%q %v，期望%q", message.Body, err, tc.body)
			}
		})
	}
}
