package main

import (
	"fmt"
	"strings"
	"time"
)

// quietHours 是每天不运行的时段，按本机时区。from 晚于 to 表示跨过午夜，比如 22:00-08:00。
type quietHours struct {
	from, to int // 从零点算起的分钟数
	on       bool
}

func (q *quietHours) String() string {
	if !q.on {
		return "off"
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", q.from/60, q.from%60, q.to/60, q.to%60)
}

func (q *quietHours) Set(v string) error {
	if v == "" || v == "off" {
		q.on = false
		return nil
	}
	a, b, ok := strings.Cut(v, "-")
	if !ok {
		return fmt.Errorf("要写成 22:00-08:00 这样")
	}
	from, err := clock(a)
	if err != nil {
		return err
	}
	to, err := clock(b)
	if err != nil {
		return err
	}
	if from == to {
		return fmt.Errorf("开始和结束不能相同")
	}
	*q = quietHours{from: from, to: to, on: true}
	return nil
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("时间要写成 HH:MM：%q", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (q quietHours) contains(t time.Time) bool {
	if !q.on {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if q.from < q.to {
		return m >= q.from && m < q.to
	}
	return m >= q.from || m < q.to
}

// end 返回 t 所在的不运行时段结束的时刻。
func (q quietHours) end(t time.Time) time.Time {
	y, mo, d := t.Date()
	e := time.Date(y, mo, d, q.to/60, q.to%60, 0, 0, t.Location())
	if !e.After(t) {
		e = e.AddDate(0, 0, 1)
	}
	return e
}
