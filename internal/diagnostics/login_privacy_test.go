package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginDOMAndAccountFieldsHideIdentityWithoutChangingProduct(t *testing.T) {
	r := openTestRecorder(t)
	const phone = "13812345678"
	const identity = "110101199001011234"
	const qr = "private-qr-credential"
	page := Page{LoginPage: true, Screenshot: []byte("local-login-pixels"), HTML: `<html><body><input type="tel" name="fm-login-id" value="` + phone + `"><input name="search" value="关键词"><div class="account-info">手机号：` + phone + ` 身份证：` + identity + `</div><script>{"phoneNum":"` + phone + `","idCardNo":"` + identity + `"}</script><img src="https://login.example.test/newlogin/qrcode/` + qr + `?id=` + qr + `&token=` + qr + `"><article>商品编号 13812345678；价格 110101199001011234 元</article></body></html>`}
	path, err := r.Bundle("login", page, []byte(`{"account":{"phoneNum":"`+phone+`","idCardNo":"`+identity+`"},"itemDO":{"title":"商品编号 `+phone+`","desc":"价格 `+identity+` 元"}}`))
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := os.ReadFile(filepath.Join(path, "page.json"))
	var metadata struct {
		LoginPage bool `json:"login_page"`
	}
	if json.Unmarshal(meta, &metadata) != nil || !metadata.LoginPage {
		t.Fatal("本机诊断包未记录登录截图标记", string(meta))
	}
	pixels, err := os.ReadFile(filepath.Join(path, "screenshot.jpg"))
	if err != nil || string(pixels) != "local-login-pixels" {
		t.Fatal("本机登录截图未原样保存", err)
	}
	html, err := os.ReadFile(filepath.Join(path, "page.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "value=\""+phone+"\"") || strings.Contains(string(html), "手机号："+phone) || strings.Contains(string(html), "身份证："+identity) || strings.Contains(string(html), `"phoneNum":"`+phone) || strings.Contains(string(html), `"idCardNo":"`+identity) || strings.Contains(string(html), qr) {
		t.Fatalf("真实登录和账号结构未脱敏：%s", html)
	}
	response, err := os.ReadFile(filepath.Join(path, "response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(response), `"phoneNum":"`+phone) || strings.Contains(string(response), `"idCardNo":"`+identity) {
		t.Fatal("账号字段未脱敏", string(response))
	}
	for _, data := range []string{string(html), string(response)} {
		if !strings.Contains(data, "商品编号 "+phone) || !strings.Contains(data, "价格 "+identity+" 元") {
			t.Fatal("商品原文被数字正则抹去", data)
		}
	}
}
