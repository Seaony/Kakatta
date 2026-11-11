package devfixture

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"testing"
	"time"
)

func TestDevelopmentAutoLoginIsOptInAndCloseJoinsIt(t *testing.T) {
	f := New(t.Context(), "http://127.0.0.1:19090")
	t.Cleanup(f.Close)
	client := &http.Client{Timeout: 2 * time.Second}
	open := func() string {
		t.Helper()
		response, err := client.Get(f.Metadata().Site.Home)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		gate := regexp.MustCompile(`/fixture/login-gate\?id=[0-9]+`).Find(body)
		if len(gate) == 0 {
			t.Fatal("页面没有创建本次假扫码")
		}
		return f.Metadata().Site.Home + string(gate[1:])
	}
	gate := open()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, gate, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(r)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("默认夹具提前完成了假扫码", err)
	}
	f.EnableDevelopmentAutoLogin()
	f.EnableDevelopmentAutoLogin()
	gate = open()
	response, err = client.Get(gate)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("开发模式没有完成当前假扫码", response.StatusCode)
	}
	f.Close()
	select {
	case <-f.autoLoginDone:
	default:
		t.Fatal("Close没有等假扫码协程退出")
	}
	f.EnableDevelopmentAutoLogin()
}
