package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/Seaony/Kakatta/internal/analysis/images"
)

func TestBusinessThumbnailUsesFindingAccountAndLocalCache(t *testing.T) {
	f := newBusinessFixture(t)
	firstTask, secondTask := resultTask(t, f), resultTask(t, f)
	finding := resultFinding(t, f, firstTask, "thumbnail", 1, false)
	other := resultFinding(t, f, secondTask, "other", 2, false)
	resultExec(t, f, `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read)
		VALUES(?,'thumbnail',1,?,0,'pending','title',0)`, secondTask, f.clock.Load())
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	var firstCalls, secondCalls atomic.Int64
	firstProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		if r.URL.Host != "img.alicdn.com" || r.URL.Path != "/cover.jpg" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("下载未保留已存的图片地址，或转发了登录凭据")
		}
		if r.Header.Get("Proxy-Authorization") != "Basic dGVzdDpwYXNz" {
			t.Error("固定代理未收到自身认证")
		}
		_, _ = w.Write(original.Bytes())
	}))
	defer firstProxy.Close()
	secondProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		_, _ = w.Write(images.SampleJPEG())
	}))
	defer secondProxy.Close()
	bindThumbnailTestProxy(t, f, firstTask, "第一个代理", firstProxy.URL, true)
	bindThumbnailTestProxy(t, f, secondTask, "第二个代理", secondProxy.URL, false)
	resultExec(t, f, `UPDATE listings SET thumbnail_url='http://img.alicdn.com/cover.jpg',is_favorite=1 WHERE item_id='thumbnail'`)
	path := "/api/v1/listings/thumbnail/thumbnail"
	findingPath := path + "?finding_id=" + strconv.FormatInt(finding, 10)
	for _, test := range []struct {
		path   string
		auth   bool
		status int
	}{
		{findingPath, false, http.StatusUnauthorized},
		{path + "?url=http://example.invalid/cover", true, http.StatusBadRequest},
		{path + "?finding_id=" + strconv.FormatInt(other, 10), true, http.StatusNotFound},
		{"/api/v1/listings/missing/thumbnail", true, http.StatusNotFound},
	} {
		requireStatus(t, f.request(http.MethodGet, test.path, "", test.auth), test.status)
	}
	if firstCalls.Load()+secondCalls.Load() != 0 {
		t.Fatal("参数或鉴权未通过却发起下载")
	}
	first := f.request(http.MethodGet, findingPath, "", true)
	requireStatus(t, first, http.StatusOK)
	if first.Header().Get("Content-Type") != "image/jpeg" || first.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("缩略图响应头不符：%v", first.Header())
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(first.Body.Bytes())); err != nil {
		t.Fatal("响应不是 JPEG", err)
	}
	for _, cachedPath := range []string{findingPath, path} {
		cached := f.request(http.MethodGet, cachedPath, "", true)
		requireStatus(t, cached, http.StatusOK)
		if !bytes.Equal(first.Body.Bytes(), cached.Body.Bytes()) {
			t.Fatal("缩略图未复用同一缓存")
		}
	}
	resultExec(t, f, `DELETE FROM tasks WHERE id IN (?,?)`, firstTask, secondTask)
	requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusOK)
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("未使用指定账号代理或重复下载：%d、%d", firstCalls.Load(), secondCalls.Load())
	}
	if _, err := f.imageCache.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(http.MethodGet, path, "", true), http.StatusNoContent)
}

func TestBusinessThumbnailSkipsUntrustedSourceAndFailedProxy(t *testing.T) {
	f := newBusinessFixture(t)
	task := resultTask(t, f)
	resultFinding(t, f, task, "thumbnail", 1, false)
	var calls, unexpected atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "img.alicdn.com" {
			unexpected.Add(1)
		}
		switch r.URL.Path {
		case "/redirect.jpg":
			http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer proxy.Close()
	bindThumbnailTestProxy(t, f, task, "本地代理", proxy.URL, false)
	path := "/api/v1/listings/thumbnail/thumbnail"
	for _, target := range []string{
		"", "http://untrusted.invalid/photo", "http://img.alicdn.com.untrusted.invalid/photo",
		"http://img.alicdn.com:1234/photo", "http://user:secret@img.alicdn.com/photo",
		"http://img.alicdn.com/redirect.jpg", "http://img.alicdn.com/failure.jpg",
	} {
		resultExec(t, f, `UPDATE listings SET thumbnail_url=? WHERE item_id='thumbnail'`, target)
		response := f.request(http.MethodGet, path, "", true)
		requireStatus(t, response, http.StatusNoContent)
		if response.Body.Len() != 0 {
			t.Fatal("失败响应返回了非空图片")
		}
	}
	if calls.Load() != 2 || unexpected.Load() != 0 {
		t.Fatalf("未阻止不受信的图片或重定向：%d、%d", calls.Load(), unexpected.Load())
	}
}

func bindThumbnailTestProxy(t *testing.T, f *businessFixture, task int64, name, address string, credential bool) {
	t.Helper()
	if credential {
		proxy, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		proxy.User = url.UserPassword("test", "pass")
		address = proxy.String()
	}
	proxyName, _ := json.Marshal(name)
	proxyURL, _ := json.Marshal(address)
	proxy, err := f.service.CreateProxy(t.Context(), map[string]json.RawMessage{"name": proxyName, "url": proxyURL})
	if err != nil {
		t.Fatal(err)
	}
	account, err := resultExec(t, f, `INSERT INTO accounts(name,proxy_id,state) VALUES(?,?,'signed_out')`, name, proxy.ID).LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	resultExec(t, f, `UPDATE tasks SET account_id=? WHERE id=?`, account, task)
}
