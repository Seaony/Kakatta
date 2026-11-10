// Package quota 按 DOMAIN.md 7.3 节检查一个闲鱼账号的访问额度：滚动 60 分钟内的总访问数，以及当天同类访问数。
// 只管判断，不管存储：记录存在哪里由调用方决定。
package quota

import (
	"fmt"
	"time"
)

// Kind 是访问的种类：首页、搜索、筛选、翻页算搜索类；详情页、卖家主页、自己的主页算详情类。
type Kind string

const (
	Search Kind = "search"
	Detail Kind = "detail"
)

// Limits 是一个账号的访问上限（CRAWL-POLICY 第六节）。
type Limits struct {
	Hourly      int // 滚动 60 分钟内的总访问数
	DailySearch int // 当天的搜索类访问数
	DailyDetail int // 当天的详情类访问数
}

// Visit 是一次已经计入的访问。
type Visit struct {
	At   time.Time `json:"at"`
	Kind Kind      `json:"kind"`
}

// Keep 和数据库额度记录的保留期一致，重启后仍能查到最近两天的访问。
const Keep = 48 * time.Hour

// Check 判断现在还能不能再访问一次 kind 类的页面。不能时返回说明原因的错误。
// 「今天」按 now 所在的时区算。
func Check(visits []Visit, now time.Time, kind Kind, l Limits) error {
	hourAgo := now.Add(-time.Hour)
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	var hour, day int
	for _, v := range visits {
		if v.At.After(hourAgo) {
			hour++
		}
		if v.Kind == kind && !v.At.Before(today) {
			day++
		}
	}
	if hour >= l.Hourly {
		return fmt.Errorf("最近 60 分钟已经访问 %d 次，上限 %d 次", hour, l.Hourly)
	}
	daily := l.DailySearch
	if kind == Detail {
		daily = l.DailyDetail
	}
	if day >= daily {
		return fmt.Errorf("今天的%s访问已经 %d 次，上限 %d 次", kindName(kind), day, daily)
	}
	return nil
}

// Prune 去掉判断时已经用不到的旧记录。
func Prune(visits []Visit, now time.Time) []Visit {
	out := visits[:0]
	for _, v := range visits {
		if now.Sub(v.At) < Keep {
			out = append(out, v)
		}
	}
	return out
}

func kindName(k Kind) string {
	if k == Detail {
		return "详情类"
	}
	return "搜索类"
}
