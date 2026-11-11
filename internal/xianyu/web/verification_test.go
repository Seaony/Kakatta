package web

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/remote"
)

func TestVerificationScreenBoxUsesBrowserOriginAndClipsToDisplay(t *testing.T) {
	for _, test := range []struct {
		box  VerificationBox
		x, y int64
		want *remote.Viewport
	}{
		{VerificationBox{X: 100.5, Y: 50.25, Width: 200, Height: 100}, 10, 80, &remote.Viewport{X: 86, Y: 106, Width: 249, Height: 149}},
		{VerificationBox{X: 1895, Y: 1040, Width: 50, Height: 50}, 0, 0, &remote.Viewport{X: 1871, Y: 1016, Width: 49, Height: 64}},
		{VerificationBox{X: -5, Y: -5, Width: 10, Height: 10}, 0, 0, &remote.Viewport{Width: 29, Height: 29}},
		{VerificationBox{X: -20, Y: 50, Width: 10, Height: 10}, 0, 0, nil},
		{VerificationBox{X: 50, Y: 50, Width: 0, Height: 10}, 0, 0, nil},
	} {
		got := verificationScreenBox(test.box, test.x, test.y)
		if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
			t.Errorf("屏幕位置不符：盒子 %+v，得到 %+v，期望 %+v", test.box, got, test.want)
		}
	}
}

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
