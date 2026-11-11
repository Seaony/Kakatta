// Package diagnostics 只在本机保存有界的排错资料，不读取账号或服务商的凭据。
package diagnostics

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

const redacted = "[已隐去]"

const privateNames = `cookie|set-cookie|authorization|proxy-authorization|password|passwd|pwd|secret|secretkey|secret_key|api_key|apikey|access_token|accesstoken|refresh_token|token|session_token|sessionid|session_id|totp_secret|totpsecret|unb|mobile|mobilephone|mobilenum|mobilephonenum|phone|phonenum|phonenumber|tel|telephone|idcard|idcardno|id_card|identitycard|identitycardno|identity_card|idnumber|certno|qrcode|qrcodedata|qrtoken`

var privateField = regexp.MustCompile(`(?i)^(?:` + privateNames + `)$`)
var privatePair = regexp.MustCompile(`(?i)((?:["']?)(?:` + privateNames + `)(?:["']?)\s*[:=]\s*)(?:"(?:\\.|[^"\\])*(?:"|$)|'[^']*(?:'|$)|[^\s,;}<>]+)`)
var headerLine = regexp.MustCompile(`(?im)(\b(?:cookie|set-cookie|authorization|proxy-authorization)\s*:\s*).*$`)
var absoluteURL = regexp.MustCompile(`https?://[^\s<>"'\\]+|socks5://[^\s<>"'\\]+`)
var accountNode = regexp.MustCompile(`(?i)(?:^|[-_\s])(?:account|login|qrcode|qrlogin|mobile|phone|identity|idcard)(?:[-_\s]|$)`)
var identityDigits = regexp.MustCompile(`[1-9][0-9]{16}[0-9Xx]|(?:\+?86[- ]?)?1[3-9][0-9]{9}`)
var identityLabel = regexp.MustCompile(`((?:手机号(?:码)?|身份证(?:号|号码)?|证件号码)\s*[:：]?\s*)(?:[+0-9Xx -]{11,25})`)

// SafeURL 只保留定位页面所需的公开参数，去掉代理认证、片段和签名。
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[网址无法解析]"
	}
	u.User, u.Fragment, u.RawFragment = nil, "", ""
	query := url.Values{}
	lower := strings.ToLower(u.Hostname() + u.Path)
	login := strings.Contains(lower, "login") || strings.Contains(lower, "qrcode") || strings.Contains(lower, "oauth")
	if !login {
		for _, name := range []string{"q", "id", "itemId", "userId", "page", "pageNumber"} {
			if values := u.Query()[name]; len(values) > 0 {
				query[name] = values
			}
		}
	}
	parts := strings.Split(u.Path, "/")
	for index, part := range parts {
		if strings.EqualFold(part, "qrcode") || strings.EqualFold(part, "qrlogin") {
			if index+1 < len(parts) {
				u.Path = strings.Join(parts[:index+1], "/") + "/" + redacted
				u.RawPath = ""
			}
			break
		}
	}
	u.RawQuery, u.ForceQuery = query.Encode(), false
	return u.String()
}

// RedactText 也处理半截响应，保留故障原文的结构和商品文字。
func RedactText(text string) string {
	text = absoluteURL.ReplaceAllStringFunc(text, SafeURL)
	text = headerLine.ReplaceAllString(text, "${1}"+redacted)
	return privatePair.ReplaceAllString(text, "${1}\""+redacted+"\"")
}

func redactJSON(data []byte) []byte {
	var value any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if dec.Decode(&value) == nil {
		value = redactValue(value)
		if encoded, err := json.Marshal(value); err == nil {
			return encoded
		}
	}
	return []byte(RedactText(string(data)))
}

func redactValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if privateField.MatchString(key) {
				v[key] = redacted
			} else {
				v[key] = redactValue(child)
			}
		}
	case []any:
		for index := range v {
			v[index] = redactValue(v[index])
		}
	case string:
		return RedactText(v)
	}
	return value
}

func redactHTML(document string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(document))
	var out strings.Builder
	type node struct {
		name    string
		private bool
	}
	stack := []node{}
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.EndTagToken {
			token := tokenizer.Token()
			for len(stack) > 0 {
				last := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if last.name == token.Data {
					break
				}
			}
			out.WriteString(token.String())
			continue
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			text := RedactText(string(tokenizer.Raw()))
			if len(stack) > 0 && stack[len(stack)-1].private {
				text = identityDigits.ReplaceAllString(text, redacted)
				text = identityLabel.ReplaceAllString(text, "${1}"+redacted)
			}
			out.WriteString(text)
			continue
		}
		token := tokenizer.Token()
		private := len(stack) > 0 && stack[len(stack)-1].private
		for _, attr := range token.Attr {
			if (attr.Key == "name" || attr.Key == "id" || attr.Key == "class") && (privateField.MatchString(attr.Val) || accountNode.MatchString(attr.Val)) {
				private = true
			}
		}
		for index := range token.Attr {
			attr := &token.Attr[index]
			if privateField.MatchString(strings.TrimPrefix(attr.Key, "data-")) || token.Data == "input" && attr.Key == "value" || private && (attr.Key == "value" || attr.Key == "content" || token.Data == "img" && (attr.Key == "src" || attr.Key == "srcset")) {
				attr.Val = redacted
			} else {
				attr.Val = RedactText(attr.Val)
			}
		}
		out.WriteString(token.String())
		if kind == html.StartTagToken && !strings.Contains(" area base br col embed hr img input link meta param source track wbr ", " "+token.Data+" ") {
			stack = append(stack, node{name: token.Data, private: private})
		}
	}
	return out.String()
}
