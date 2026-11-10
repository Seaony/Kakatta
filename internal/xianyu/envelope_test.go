package xianyu

import (
	"os"
	"reflect"
	"testing"
)

func TestCheckEnvelope(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    RetKind
		wantRet []string
		wantErr bool
	}{
		{"成功", `{"ret":["SUCCESS::调用成功"],"data":{}}`, RetOK, []string{"SUCCESS::调用成功"}, false},
		{"成功但不是第一项", `{"ret":["FAIL_BIZ_X::失败","SUCCESS::调用成功"]}`, RetBizFail, []string{"FAIL_BIZ_X::失败", "SUCCESS::调用成功"}, false},
		{"令牌过期（mtop 原样拼错）", `{"ret":["FAIL_SYS_TOKEN_EXOIRED::令牌过期"]}`, RetTokenExpired, []string{"FAIL_SYS_TOKEN_EXOIRED::令牌过期"}, false},
		{"令牌过期", `{"ret":["FAIL_SYS_TOKEN_EXPIRED::令牌过期"]}`, RetTokenExpired, []string{"FAIL_SYS_TOKEN_EXPIRED::令牌过期"}, false},
		{"令牌为空", `{"ret":["FAIL_SYS_TOKEN_EMPTY::令牌为空"]}`, RetTokenExpired, []string{"FAIL_SYS_TOKEN_EMPTY::令牌为空"}, false},
		{"人机验证", `{"ret":["FAIL_SYS_USER_VALIDATE::哎哟喂"],"data":{}}`, RetNeedVerify, []string{"FAIL_SYS_USER_VALIDATE::哎哟喂"}, false},
		{"RGV587", `{"ret":["RGV587_ERROR::SM::被挤爆啦"]}`, RetNeedVerify, []string{"RGV587_ERROR::SM::被挤爆啦"}, false},
		{"验证码不在第一项", `{"ret":["SUCCESS::调用成功","RGV587_ERROR::SM"]}`, RetNeedVerify, []string{"SUCCESS::调用成功", "RGV587_ERROR::SM"}, false},
		{"处罚页要验证码",
			`{"ret":["FAIL_SYS_ILLEGAL_ACCESS::非法请求"],"data":{"url":"https://h5api.m.goofish.com//h5/_____tmd_____/punish?x5secdata=abc&x5step=2&action=captcha"}}`,
			RetNeedVerify, []string{"FAIL_SYS_ILLEGAL_ACCESS::非法请求"}, false},
		{"处罚页直接拒绝",
			`{"ret":["FAIL_SYS_ILLEGAL_ACCESS::非法请求"],"data":{"url":"https://h5api.m.goofish.com//h5/_____tmd_____/punish?x5secdata=abc&action=deny"}}`,
			RetDenied, []string{"FAIL_SYS_ILLEGAL_ACCESS::非法请求"}, false},
		{"拒绝优先于验证码",
			`{"ret":["RGV587_ERROR::SM"],"data":{"url":"https://x/punish?action=deny"}}`,
			RetDenied, []string{"RGV587_ERROR::SM"}, false},
		{"处罚页网址大小写不同", `{"ret":["SUCCESS::调用成功"],"data":{"url":"https://x/PUNISH?Action=Deny"}}`, RetDenied, []string{"SUCCESS::调用成功"}, false},
		{"处罚页没带 action 也拦住", `{"ret":["SUCCESS::调用成功"],"data":{"url":"https://x/_____tmd_____/punish?x5secdata=abc"}}`, RetNeedVerify, []string{"SUCCESS::调用成功"}, false},
		{"处罚页没有 ret 照样报风控", `{"data":{"url":"https://x/punish?action=captcha"}}`, RetNeedVerify, nil, false},
		{"普通 data.url 不算风控", `{"ret":["SUCCESS::调用成功"],"data":{"url":"https://www.goofish.com/item?id=1"}}`, RetOK, []string{"SUCCESS::调用成功"}, false},
		{"验证优先于登录失效", `{"ret":["FAIL_SYS_SESSION_EXPIRED::过期","FAIL_SYS_USER_VALIDATE"]}`, RetNeedVerify, []string{"FAIL_SYS_SESSION_EXPIRED::过期", "FAIL_SYS_USER_VALIDATE"}, false},
		{"登录失效", `{"ret":["FAIL_SYS_SESSION_EXPIRED::Session过期"]}`, RetSessionExpired, []string{"FAIL_SYS_SESSION_EXPIRED::Session过期"}, false},
		{"登录失效优先于令牌过期", `{"ret":["FAIL_SYS_TOKEN_EXPIRED","FAIL_SYS_SESSION_EXPIRED"]}`, RetSessionExpired, []string{"FAIL_SYS_TOKEN_EXPIRED", "FAIL_SYS_SESSION_EXPIRED"}, false},
		{"限流码当业务失败", `{"ret":["FAIL_SYS_TRAFFIC_LIMIT::被限流"]}`, RetBizFail, []string{"FAIL_SYS_TRAFFIC_LIMIT::被限流"}, false},
		{"其他失败", `{"ret":["FAIL_BIZ_ITEM_NOT_EXIST::商品不存在"]}`, RetBizFail, []string{"FAIL_BIZ_ITEM_NOT_EXIST::商品不存在"}, false},
		{"ret 是空数组", `{"ret":[]}`, RetBizFail, nil, false},
		{"JSONP 外壳", ` mtopjsonp3({"ret":["SUCCESS::调用成功"]}); `, RetOK, []string{"SUCCESS::调用成功"}, false},
		{"ret 缺失", `{"data":{"resultList":[]}}`, RetBizFail, nil, true},
		{"ret 不是数组", `{"ret":"SUCCESS"}`, RetBizFail, nil, true},
		{"根是数组（上游卖家样例）", `[{"cardData":{}}]`, RetBizFail, nil, true},
		{"不是 JSON", `<html>非法访问</html>`, RetBizFail, nil, true},
		{"空正文", ``, RetBizFail, nil, true},
		{"JSON 后面有多余内容", `{"ret":["SUCCESS"]}{"ret":["SUCCESS"]}`, RetBizFail, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, ret, err := CheckEnvelope([]byte(c.body))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v，期望出错 = %v", err, c.wantErr)
			}
			if kind != c.want {
				t.Errorf("kind = %v，期望 %v", kind, c.want)
			}
			if !reflect.DeepEqual(ret, c.wantRet) {
				t.Errorf("ret = %#v，期望 %#v", ret, c.wantRet)
			}
		})
	}
}

// 上游手写样例没有 ret：解析函数能吃，但真实响应入口要当成解析失败（XIANYU.md 2.4 节）。
func TestCheckEnvelopeRejectsUpstreamFixtures(t *testing.T) {
	for _, name := range []string{"upstream_search_results.json", "upstream_user_head.json", "upstream_user_items.json", "upstream_ratings.json"} {
		if _, _, err := CheckEnvelope(readFixture(t, name)); err == nil {
			t.Errorf("%s 没有 ret，应当报错", name)
		}
	}
}

func TestCheckEnvelopeFixtures(t *testing.T) {
	for _, name := range []string{"search.json", "search_jsonp.txt", "detail.json", "seller_head.json", "seller_items.json", "seller_ratings.json"} {
		kind, _, err := CheckEnvelope(readFixture(t, name))
		if err != nil || kind != RetOK {
			t.Errorf("%s：kind = %v，err = %v，期望成功", name, kind, err)
		}
	}
}

func TestStripJSONP(t *testing.T) {
	cases := []struct{ in, want string }{
		{`mtopjsonp1({"a":1})`, `{"a":1}`},
		{`  mtopjsonp1 ( {"a":1} ) ;  `, `{"a":1}`},
		{"cb_$2({\"a\":[1]});\n", `{"a":[1]}`},
		{`window.lib.cb([1,2])`, `[1,2]`},
		// 不是 JSONP 的原样返回。
		{` {"a":1} `, ` {"a":1} `},
		{`[1]`, `[1]`},
		{`1cb({"a":1})`, `1cb({"a":1})`},
		{`cb {"a":1}`, `cb {"a":1}`},
		{`cb({"a":1}`, `cb({"a":1}`},
		{`<html></html>`, `<html></html>`},
		{``, ``},
	}
	for _, c := range cases {
		if got := string(StripJSONP([]byte(c.in))); got != c.want {
			t.Errorf("StripJSONP(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
