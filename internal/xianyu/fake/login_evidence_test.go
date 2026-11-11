package fake

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestAutomaticLoginGateWaitsForEachActualScan(t *testing.T) {
	f := Start()
	t.Cleanup(f.Close)
	f.EnableAutomaticLogin()
	open := func() uint64 {
		t.Helper()
		response, err := http.Get(f.Home())
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.loginGateNext
	}
	first := open()
	// 程序看见框时，页面自己的等待请求可能尚未到达服务端。
	f.CompleteAutomaticLogin()
	response, err := http.Get(f.Home() + "fixture/login-gate?id=" + strconv.FormatUint(first, 10))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("已确认的本次扫码必须放行后来才到的等待请求：%d", response.StatusCode)
	}
	second := open()
	if second <= first {
		t.Fatal("新窗口必须重新等待自己的扫码")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Home()+"fixture/login-gate?id="+strconv.FormatUint(second, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("上一回扫码不能提前放行之后的新窗口：%v", err)
	}
}
