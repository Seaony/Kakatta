// Package pacer 生成模拟真人的随机等待。所有等待都经过这里，区间来自配置项（docs/CRAWL-POLICY.md 第六节）。
package pacer

import (
	"context"
	"math/rand/v2"
	"time"
)

// Range 是一段等待区间。结果在 [Min, Max] 里均匀分布，永远不低于 Min。
type Range struct {
	Min time.Duration
	Max time.Duration
}

// Between 是 Range 的简写。
func Between(min, max time.Duration) Range { return Range{Min: min, Max: max} }

// Clock 抽象出时间和随机数，测试时可以换成不真的等待的版本。
type Clock interface {
	Sleep(ctx context.Context, d time.Duration) error
	Float64() float64
}

// Pacer 按区间随机等待。
type Pacer struct{ clock Clock }

// New 用给定的时钟创建；clock 为 nil 时用真实时钟。
func New(clock Clock) *Pacer {
	if clock == nil {
		clock = RealClock{}
	}
	return &Pacer{clock: clock}
}

// Pick 在区间里取一个值。
func (p *Pacer) Pick(r Range) time.Duration {
	if r.Max <= r.Min {
		return r.Min
	}
	return r.Min + time.Duration(p.clock.Float64()*float64(r.Max-r.Min))
}

// Rand 返回 [0, 1) 的随机数，给需要随机位置、随机步数的地方用。
func (p *Pacer) Rand() float64 { return p.clock.Float64() }

// Wait 在区间里随机取一个时长并等待；ctx 取消时立即返回。
func (p *Pacer) Wait(ctx context.Context, r Range) error {
	return p.clock.Sleep(ctx, p.Pick(r))
}

// Stretch 把间隔随机延长 0 到 ratio（比如 0.3 表示最多延长 30%），只加不减。
func (p *Pacer) Stretch(d time.Duration, ratio float64) time.Duration {
	if ratio <= 0 {
		return d
	}
	return d + time.Duration(p.clock.Float64()*ratio*float64(d))
}

// RealClock 是真实的时钟。
type RealClock struct{}

// Sleep 等待 d，ctx 取消时提前返回 ctx 的错误。
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Float64 返回 [0, 1) 的随机数。
func (RealClock) Float64() float64 { return rand.Float64() }
