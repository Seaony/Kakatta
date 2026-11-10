package fake_test

import (
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestFixtureResourcesStayOnLocalServer(t *testing.T) {
	f := fake.Start()
	defer f.Close()
	resp, err := http.Post(f.Home()+"h5/mtop.taobao.idlemtopsearch.pc.search/1.0/", "application/x-www-form-urlencoded", strings.NewReader("q=相机"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	items, err := xianyu.ParseSearch(body)
	if err != nil || len(items) != 2 {
		t.Fatalf("样例搜索结果：%v，%d 件", err, len(items))
	}
	local, _ := url.Parse(f.Home())
	for _, item := range items {
		imageURL, err := url.Parse(item.Image)
		if err != nil || imageURL.Host != local.Host {
			t.Fatalf("样例主图必须来自本机：%s", item.Image)
		}
		imageResp, err := http.Get(item.Image)
		if err != nil {
			t.Fatal(err)
		}
		_, err = jpeg.Decode(imageResp.Body)
		imageResp.Body.Close()
		if err != nil {
			t.Fatalf("样例图片必须能解码：%v", err)
		}
	}
}

func TestFixturePrefillIsEscapedAndPageBlocksExternalResources(t *testing.T) {
	f := fake.Start()
	defer f.Close()
	f.SetPrefill(`" onfocus="location.href='https://invalid.example'`)
	resp, err := http.Get(f.Home())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `value="" onfocus=`) {
		t.Fatal("预填文字不能变成新的 HTML 属性")
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatal("假页面必须限制请求只能发到自己")
	}
}
