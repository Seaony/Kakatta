package images

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrepareSharesSlotsAcrossDifferentPreparers(t *testing.T) {
	picture := prepareFixtureJPEG(t)
	for _, canceled := range []bool{false, true} {
		name := "完成"
		if canceled {
			name = "取消"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			entered := make(chan struct{}, 4)
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			var active, maximum, calls atomic.Int32
			var perHTTP [2]atomic.Int32
			done := make(chan error, 2)
			for index := range 2 {
				p := newTestPreparer(t, Dependencies{HTTP: prepareDoer(func(r *http.Request) (*http.Response, error) {
					perHTTP[index].Add(1)
					calls.Add(1)
					now := active.Add(1)
					for old := maximum.Load(); now > old && !maximum.CompareAndSwap(old, now); old = maximum.Load() {
					}
					defer active.Add(-1)
					entered <- struct{}{}
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-release:
						return successResponse(picture), nil
					}
				})})
				go func() {
					result, err := p.Prepare(ctx, []string{"https://images.invalid/1", "https://images.invalid/2"}, 2)
					if err == nil && len(result.Images) != 2 {
						t.Errorf("正常完成丢失图片：%+v", result)
					}
					done <- err
				}()
			}
			for range 3 {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("不同准备器未共享启动三张图")
				}
			}
			select {
			case <-entered:
				t.Fatal("不同账号传输同时下载超过三张图")
			case <-time.After(20 * time.Millisecond):
			}
			if perHTTP[0].Load() == 0 || perHTTP[1].Load() == 0 {
				t.Fatal("回归没有覆盖两份独立传输")
			}
			if canceled {
				cancel()
			} else {
				releaseOnce.Do(func() { close(release) })
			}
			for range 2 {
				select {
				case err := <-done:
					if canceled && !errors.Is(err, context.Canceled) || !canceled && err != nil {
						t.Fatalf("完成或取消语义错误：%v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("准备器没有完成并等待下载退出")
				}
			}
			wantCalls := int32(4)
			if canceled {
				wantCalls = 3
			}
			if maximum.Load() != 3 || active.Load() != 0 || len(downloadSlots) != 0 || calls.Load() != wantCalls {
				t.Fatalf("全局下载名额未正确限制或释放：max%d active%d slots%d calls%d", maximum.Load(), active.Load(), len(downloadSlots), calls.Load())
			}
		})
	}
}
