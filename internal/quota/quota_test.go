package quota

import (
	"testing"
	"time"
)

var limits = Limits{Hourly: 3, DailySearch: 4, DailyDetail: 2}

func at(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.Local) }

func TestHourlyIsRollingAcrossKinds(t *testing.T) {
	v := []Visit{{at(9, 10), Search}, {at(9, 20), Detail}, {at(9, 50), Search}}
	if err := Check(v, at(10, 5), Search, limits); err == nil {
		t.Fatal("60 分钟内已有 3 次，应该拒绝")
	}
	// 9:10 那次滑出窗口后又可以访问。
	if err := Check(v, at(10, 11), Search, limits); err != nil {
		t.Fatal(err)
	}
}

func TestDailyCountsOnlySameKindToday(t *testing.T) {
	yesterday := at(23, 0).AddDate(0, 0, -1)
	v := []Visit{{yesterday, Detail}, {at(1, 0), Detail}, {at(2, 0), Search}}
	if err := Check(v, at(12, 0), Detail, limits); err != nil {
		t.Fatalf("今天只有 1 次详情类访问：%v", err)
	}
	v = append(v, Visit{at(3, 0), Detail})
	if err := Check(v, at(12, 0), Detail, limits); err == nil {
		t.Fatal("今天详情类已满 2 次，应该拒绝")
	}
	if err := Check(v, at(12, 0), Search, limits); err != nil {
		t.Fatalf("搜索类不受详情类上限影响：%v", err)
	}
}

func TestPruneKeepsRecent(t *testing.T) {
	now := at(12, 0)
	v := []Visit{{now.Add(-26 * time.Hour), Search}, {now.Add(-time.Hour), Search}}
	if got := Prune(v, now); len(got) != 1 || !got[0].At.Equal(now.Add(-time.Hour)) {
		t.Fatalf("Prune 结果不对：%v", got)
	}
}
