package fake

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSearchFaultIsScopedAndReturnsTruncatedJSON(t *testing.T) {
	f := Start()
	t.Cleanup(f.Close)
	gate := f.SetSearchFault("故障关键词", SearchFault{Final: true, TruncatedJSON: true})
	for _, test := range []struct {
		keyword, query string
		valid          bool
	}{
		{"其它关键词", "final=1", true}, {"故障关键词", "first=1", true}, {"故障关键词", "final=1", false},
	} {
		response, err := http.Post(f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?"+test.query, "text/plain", strings.NewReader("q="+url.QueryEscape(test.keyword)))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || json.Valid(body) != test.valid {
			t.Fatalf("故障没有准确匹配请求：%+v，HTTP=%d，err=%v，body=%s", test, response.StatusCode, err, body)
		}
	}
	select {
	case <-gate.Entered:
	case <-time.After(time.Second):
		t.Fatal("缺少目标请求进入证据")
	}
	if f.RequestCount() != 3 {
		t.Fatal("没有记录全部访问", f.RequestCount())
	}
}

func TestSearchFaultHoldCancelsAndClearDoesNotReleaseInFlight(t *testing.T) {
	f := Start()
	t.Cleanup(f.Close)
	gate := f.SetSearchFault("挂起", SearchFault{First: true, Hold: true})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?first=1", strings.NewReader("q="+url.QueryEscape("挂起")))
		if err == nil {
			var response *http.Response
			response, err = http.DefaultClient.Do(request)
			if response != nil {
				response.Body.Close()
			}
		}
		done <- err
	}()
	select {
	case <-gate.Entered:
	case <-time.After(time.Second):
		t.Fatal("请求没进入挂起状态")
	}
	f.ClearSearchFault("挂起")
	select {
	case <-gate.Done:
		t.Fatal("移除配置提前释放了在途请求")
	default:
	}
	response, err := http.Post(f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?first=1", "text/plain", strings.NewReader("q="+url.QueryEscape("挂起")))
	if err != nil {
		t.Fatal("故障配置影响之后的正常请求", err)
	}
	response.Body.Close()
	cancel()
	select {
	case <-gate.Done:
	case <-time.After(time.Second):
		t.Fatal("请求取消后服务端没有结束")
	}
	if err := <-done; err == nil {
		t.Fatal("挂起请求没有按取消结束")
	}
}

func TestSearchFaultCloseReleasesHeldRequest(t *testing.T) {
	f := Start()
	t.Cleanup(f.Close)
	gate := f.SetSearchFault("关闭清理", SearchFault{First: true, Hold: true})
	clientDone := make(chan error, 1)
	go func() {
		response, err := http.Post(f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?first=1", "text/plain", strings.NewReader("q="+url.QueryEscape("关闭清理")))
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-gate.Entered:
	case <-time.After(time.Second):
		t.Fatal("关闭测试没有进入挂起请求")
	}
	f.ClearSearchFault("关闭清理")
	closed := make(chan struct{})
	go func() { f.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("移除配置的在途请求没有被Close清理")
	}
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
}
