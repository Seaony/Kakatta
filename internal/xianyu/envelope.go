// Package xianyu 解析闲鱼接口的响应：外壳（ret、data.url）、搜索结果、详情、卖家。
// 字段路径、默认值和换算规则以 docs/XIANYU.md 第二节和第九节为准。解析函数都是纯函数，不访问网络。
package xianyu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// RetKind 是接口响应外壳的判断结果（XIANYU.md 2.4 节、第四节信号表）。
type RetKind int

const (
	RetOK             RetKind = iota // ret 第一项以 SUCCESS 开头
	RetTokenExpired                  // 三个令牌过期码之一：忽略这条，等下一条
	RetNeedVerify                    // FAIL_SYS_USER_VALIDATE、RGV587，或 data.url 是处罚页（action=captcha 等）
	RetDenied                        // data.url 含 punish 且含 action=deny
	RetSessionExpired                // FAIL_SYS_SESSION_EXPIRED
	RetBizFail                       // 其他非 SUCCESS
)

func (k RetKind) String() string {
	switch k {
	case RetOK:
		return "成功"
	case RetTokenExpired:
		return "令牌过期"
	case RetNeedVerify:
		return "需要人机验证"
	case RetDenied:
		return "直接拒绝"
	case RetSessionExpired:
		return "登录失效"
	case RetBizFail:
		return "业务失败"
	}
	return fmt.Sprintf("RetKind(%d)", int(k))
}

// 令牌过期码。第一个是 mtop 原样的拼写错误，不要改。
var tokenExpiredCodes = []string{"FAIL_SYS_TOKEN_EXOIRED", "FAIL_SYS_TOKEN_EXPIRED", "FAIL_SYS_TOKEN_EMPTY"}

// 需要人机验证的 ret 码。
var verifyCodes = []string{"FAIL_SYS_USER_VALIDATE", "RGV587"}

// CheckEnvelope 先去掉 JSONP 外壳（2.5 节），再读顶层 ret 和 data.url 判断类型。
// ret 缺失或正文不是 JSON 时返回 error。ret 原样返回给调用方写日志。
// 判断优先级：拒绝 > 验证 > 登录失效 > 令牌过期 > 成功 > 业务失败。
// data.url 指向处罚页时，即使 ret 缺失也照样报风控，不报 error，免得调用方把风控当普通解析失败跳过。
func CheckEnvelope(body []byte) (kind RetKind, ret []string, err error) {
	root, err := decodeJSON(body)
	if err != nil {
		return RetBizFail, nil, err
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return RetBizFail, nil, errors.New("接口正文的根不是 JSON 对象")
	}

	rawRet, hasRet := obj["ret"].([]any)
	for _, r := range rawRet {
		ret = append(ret, asScalarText(r))
	}

	// 网址统一转小写再比较，大小写不同也算命中。
	punishURL := strings.ToLower(asText(pathGet(obj, "data", "url")))
	punished := strings.Contains(punishURL, "punish")
	switch {
	case punished && strings.Contains(punishURL, "action=deny"):
		return RetDenied, ret, nil
	case retContains(ret, verifyCodes...):
		return RetNeedVerify, ret, nil
	case punished:
		// 处罚页除了拒绝以外都按需要验证处理：action=captcha 是文档里的信号，
		// 没带 action 的处罚页也不是正常响应，拿不准时宁可拦住。
		return RetNeedVerify, ret, nil
	}

	if !hasRet {
		return RetBizFail, nil, errors.New("接口正文缺少 ret 字段")
	}
	switch {
	case retContains(ret, "FAIL_SYS_SESSION_EXPIRED"):
		return RetSessionExpired, ret, nil
	case retContains(ret, tokenExpiredCodes...):
		return RetTokenExpired, ret, nil
	case len(ret) > 0 && strings.HasPrefix(ret[0], "SUCCESS"):
		return RetOK, ret, nil
	}
	return RetBizFail, ret, nil
}

// retContains 判断 ret 的任一项是否含任一个码。上游是把整个 ret 转成字符串找子串，效果相同。
func retContains(ret []string, codes ...string) bool {
	for _, r := range ret {
		for _, c := range codes {
			if strings.Contains(r, c) {
				return true
			}
		}
	}
	return false
}

// StripJSONP 去掉「回调名(...)」外壳，不是 JSONP 时原样返回。
// 第一个非空白字符是 { 或 [ 时一定不是 JSONP；回调名只认 JS 标识符（允许带点，如 a.b）。
func StripJSONP(body []byte) []byte {
	s := bytes.TrimSpace(body)
	if len(s) == 0 || s[0] == '{' || s[0] == '[' {
		return body
	}
	i := 0
	for i < len(s) && isIdentByte(s[i], i == 0) {
		i++
	}
	if i == 0 {
		return body
	}
	rest := bytes.TrimLeft(s[i:], " \t\r\n")
	if len(rest) == 0 || rest[0] != '(' {
		return body
	}
	inner := bytes.TrimSpace(rest[1:])
	inner = bytes.TrimSpace(bytes.TrimSuffix(inner, []byte(";")))
	if len(inner) == 0 || inner[len(inner)-1] != ')' {
		return body
	}
	return bytes.TrimSpace(inner[:len(inner)-1])
}

func isIdentByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '$':
		return true
	case c >= '0' && c <= '9', c == '.':
		return !first
	}
	return false
}

// decodeJSON 去掉 JSONP 外壳后解析成通用结构。数字保留成 json.Number，避免大整数（商品 ID、毫秒时间戳）丢精度。
// 正文后面还有多余内容时报错，防止把半截或拼接的正文当成有效数据。
func decodeJSON(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(StripJSONP(body)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("接口正文不是有效的 JSON：%w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("接口正文的 JSON 后面还有多余内容")
	}
	return v, nil
}
