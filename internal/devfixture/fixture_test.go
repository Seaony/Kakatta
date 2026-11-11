package devfixture_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/devfixture"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func fixtureRequest(t *testing.T, method, address, body string) (int, []byte) {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, address, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func TestFaultModesPreserveValidListsAndRecoverWithoutChangingConfiguration(t *testing.T) {
	f := devfixture.New(t.Context(), "http://127.0.0.1:19090")
	t.Cleanup(f.Close)
	control := httptest.NewServer(f.Handler())
	t.Cleanup(control.Close)
	metadata := f.Metadata()
	if metadata.ControlURL != "http://127.0.0.1:19090" || !strings.HasPrefix(metadata.Site.Home, "http://127.0.0.1:") || metadata.Site.APIHost == "" {
		t.Fatal("本地夹具元数据不完整")
	}
	searchURL := metadata.Site.Home + "h5/mtop.taobao.idlemtopsearch.pc.search/1.0/?first=1"
	setFault := func(t *testing.T, mode string) {
		t.Helper()
		status, _ := fixtureRequest(t, http.MethodPost, control.URL+"/control", `{"action":"fault","mode":"`+mode+`"}`)
		if status != http.StatusOK {
			t.Fatal("设置故障模式失败", mode, status)
		}
	}
	for _, mode := range []string{"expired", "risk", "missing", "api_error"} {
		t.Run(mode, func(t *testing.T) {
			setFault(t, mode)
			status, body := fixtureRequest(t, http.MethodPost, searchURL, "q=MacBook+Pro")
			switch mode {
			case "expired":
				if status != http.StatusOK || !bytes.Contains(body, []byte("SESSION_EXPIRED")) {
					t.Fatal("搜索没有返回登录失效", status)
				}
			case "risk":
				_, page := fixtureRequest(t, http.MethodGet, metadata.Site.Home+"search?q=MacBook+Pro", "")
				if status != http.StatusOK || !bytes.Contains(page, []byte(`class="baxia-dialog-mask" style="width:200px;height:200px;display:block"`)) {
					t.Fatal("搜索页面没有可见的验证遮罩")
				}
			case "missing":
				items, err := xianyu.ParseSearch(body)
				if status != http.StatusOK || err != nil || len(items) != 3 {
					t.Fatal("缺字段模式破坏了合法商品列表", status, err)
				}
				for _, item := range items {
					if item.ID == "" || item.Title == "" || item.PriceKnown || item.SellerNick != "" || item.Area != "" || item.Image != "" {
						t.Fatal("缺字段模式没有仅清除可选字段", item)
					}
				}
			case "api_error":
				if status != http.StatusServiceUnavailable {
					t.Fatal("搜索接口没有返回503", status)
				}
			}
			setFault(t, "normal")
			status, body = fixtureRequest(t, http.MethodPost, searchURL, "q=MacBook+Pro")
			items, err := xianyu.ParseSearch(body)
			if status != http.StatusOK || err != nil || len(items) != 3 || !items[0].PriceKnown || items[0].SellerNick == "" {
				t.Fatal("恢复正常后仍残留故障", status, err)
			}
		})
	}
	if f.Metadata() != metadata {
		t.Fatal("故障切换改动了夹具地址或凭据")
	}
	data, err := json.Marshal(f.State())
	if err != nil || bytes.Contains(data, []byte(metadata.ModelKey)) {
		t.Fatal("状态快照包含密钥或无法序列化")
	}
}

func TestControlsRemainCompatibleAndCanBeReadConcurrently(t *testing.T) {
	f := devfixture.New(t.Context(), "http://127.0.0.1:19090")
	t.Cleanup(f.Close)
	control := httptest.NewServer(f.Handler())
	t.Cleanup(control.Close)
	for _, body := range []string{`{"action":"fault","mode":"invalid"}`, `{"action":"model","mode":"invalid"}`, `{"action":"push","mode":"invalid"}`, `{"action":"risk","mode":"invalid"}`, `{"action":"search","pages":[[],[],[]]}`, `{"action":"unknown"}`} {
		if status, _ := fixtureRequest(t, http.MethodPost, control.URL+"/control", body); status != http.StatusBadRequest {
			t.Fatal("无效控制参数没有拒绝", body, status)
		}
	}
	for _, mode := range []string{"fail", "ok"} {
		if status, _ := fixtureRequest(t, http.MethodPost, control.URL+"/control", `{"action":"push","mode":"`+mode+`"}`); status != http.StatusOK {
			t.Fatal("旧推送控制失效", status)
		}
		status, _ := fixtureRequest(t, http.MethodPost, f.Metadata().PushURL, `{"kind":"test","body":"本地渠道测试"}`)
		if mode == "fail" && status != http.StatusServiceUnavailable || mode == "ok" && status != http.StatusNoContent {
			t.Fatal("推送模式与真实响应不符", mode, status)
		}
	}
	var wait sync.WaitGroup
	for range 4 {
		wait.Go(func() {
			client := &http.Client{Timeout: 3 * time.Second}
			for range 10 {
				encoded, err := json.Marshal(f.State())
				if err != nil || len(encoded) == 0 {
					t.Error("并发状态快照无效", err)
				}
				for _, call := range []struct{ address, body string }{
					{control.URL + "/control", `{"action":"fault","mode":"missing"}`},
					{control.URL + "/control", `{"action":"fault","mode":"normal"}`},
					{f.Metadata().Site.Home + "h5/mtop.taobao.idlemtopsearch.pc.search/1.0/", "q=MacBook+Pro"},
				} {
					response, err := client.Post(call.address, "application/json", strings.NewReader(call.body))
					if err != nil {
						t.Error("并发本地请求失败", err)
						return
					}
					_, err = io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if err != nil || response.StatusCode != http.StatusOK {
						t.Error("并发本地请求未成功", response.StatusCode, err)
					}
				}
			}
		})
	}
	wait.Wait()
}
