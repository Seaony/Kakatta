package pacer

import (
	"context"
	"testing"
	"time"
)

// fixedClock 不真的等待，随机数按顺序给出。
type fixedClock struct {
	vals  []float64
	i     int
	slept []time.Duration
}

func (c *fixedClock) Sleep(_ context.Context, d time.Duration) error {
	c.slept = append(c.slept, d)
	return nil
}

func (c *fixedClock) Float64() float64 {
	v := c.vals[c.i%len(c.vals)]
	c.i++
	return v
}

func TestPickStaysInRange(t *testing.T) {
	r := Between(10*time.Second, 15*time.Second)
	for _, v := range []float64{0, 0.5, 0.999999} {
		p := New(&fixedClock{vals: []float64{v}})
		got := p.Pick(r)
		if got < r.Min || got > r.Max {
			t.Fatalf("随机数 %v 取到 %v，超出区间 %v–%v", v, got, r.Min, r.Max)
		}
	}
	if got := New(&fixedClock{vals: []float64{0.7}}).Pick(Between(3*time.Second, 3*time.Second)); got != 3*time.Second {
		t.Fatalf("上下限相同时应该返回下限，得到 %v", got)
	}
}

func TestStretchOnlyLengthens(t *testing.T) {
	d := 30 * time.Minute
	for _, v := range []float64{0, 0.5, 0.999} {
		got := New(&fixedClock{vals: []float64{v}}).Stretch(d, 0.3)
		if got < d || got > d+9*time.Minute {
			t.Fatalf("随机数 %v 延长到 %v，应该在 30–39 分钟之间", v, got)
		}
	}
}

func TestWaitUsesPickedDuration(t *testing.T) {
	c := &fixedClock{vals: []float64{0.5}}
	if err := New(c).Wait(context.Background(), Between(2*time.Second, 4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(c.slept) != 1 || c.slept[0] != 3*time.Second {
		t.Fatalf("应该等 3 秒，实际 %v", c.slept)
	}
}

func TestRealClockStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := (RealClock{}).Sleep(ctx, time.Hour); err == nil {
		t.Fatal("取消后应该返回错误")
	}
	if time.Since(start) > time.Second {
		t.Fatal("取消后没有立即返回")
	}
}
