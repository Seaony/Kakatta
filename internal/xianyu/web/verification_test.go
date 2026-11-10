package web

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCurrentVerificationBoxOnlyReadsVisibleMask(t *testing.T) {
	f := newFake(t)
	f.SetMaskOnSearch(true)
	r, _, ctx := startRunner(t, f)
	if err := r.S.Navigate(f.Home(), 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if box, err := r.CurrentVerificationBox(ctx); err != nil || box != nil {
		t.Fatalf("没有可见验证框应返回空：%+v %v", box, err)
	}
	mark := r.S.Capture.Mark()
	if err := r.S.Navigate(f.Home()+"search?q=相机", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := r.S.Capture.WaitAfter(ctx, mark, r.Site.searchAPI(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	before := r.S.Capture.Mark()
	box, err := r.CurrentVerificationBox(ctx)
	if err != nil || box == nil {
		t.Fatalf("应返回当前可见验证框：%+v %v", box, err)
	}
	if box.Width != 200 || box.Height != 200 || box.X < 0 || box.Y < 0 {
		t.Fatalf("框的位置或大小不对：%+v", box)
	}
	if after := r.S.Capture.Mark(); after != before {
		t.Fatalf("定位不能产生额外访问：%d→%d", before, after)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.CurrentVerificationBox(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后不能继续定位：%v", err)
	}
}
