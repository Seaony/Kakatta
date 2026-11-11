package locales

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMessageUsesObjectForEmptyArguments(t *testing.T) {
	for _, args := range []map[string]any{nil, {}} {
		b, err := json.Marshal(Message{Code: "common.cancel", Args: args})
		if err != nil || string(b) != `{"code":"common.cancel","args":{}}` {
			t.Fatalf("空参数必须输出对象：%s，%v", b, err)
		}
	}
}

func TestSystemEventMessagesHaveBothLanguages(t *testing.T) {
	for _, tc := range []struct {
		code string
		args map[string]any
		zh   string
		en   string
	}{
		{"security.login_failures", map[string]any{"count": 1, "ip": "192.0.2.1"}, "登录失败已达到上限 1 次，最近一次来自 IP 192.0.2.1。如果不是你本人，请改一个更强的密码并开启两步验证。", "Sign-in failures have reached the limit of 1 attempt. The latest attempt came from IP 192.0.2.1. If this was not you, choose a stronger password and enable two-step verification."},
		{"security.login_failures", map[string]any{"count": 20, "ip": "2001:db8::1"}, "登录失败已达到上限 20 次，最近一次来自 IP 2001:db8::1。如果不是你本人，请改一个更强的密码并开启两步验证。", "Sign-in failures have reached the limit of 20 attempts. The latest attempt came from IP 2001:db8::1. If this was not you, choose a stronger password and enable two-step verification."},
		{"events.reset", nil, "事件记录已更新，请重新读取最新数据。", "The event history has changed. Reload the latest data."},
	} {
		for _, lang := range []Language{ZhCN, En, Ja} {
			want := tc.zh
			if lang == En {
				want = tc.en
			}
			message := Message{Code: tc.code, Args: tc.args}
			got, err := Format(lang, message)
			if lang == Ja {
				if err != nil || got == "" || got == tc.zh || got == tc.en {
					t.Errorf("日文系统事件没有独立译文：%s，%q，%v", tc.code, got, err)
				}
				for _, value := range tc.args {
					if !strings.Contains(got, fmt.Sprint(value)) {
						t.Errorf("日文系统事件丢失事实参数：%s，%v", got, value)
					}
				}
				continue
			}
			if err != nil || got != want {
				t.Errorf("系统事件%s/%s：%q，%v", tc.code, lang, got, err)
			}
		}
	}
	for _, args := range []map[string]any{nil, {"count": 20}, {"ip": "192.0.2.1"}} {
		if got, err := Format(En, Message{Code: "security.login_failures", Args: args}); err == nil || got != "" {
			t.Errorf("登录失败事件缺少事实仍显示文案：%q，%v", got, err)
		}
	}
}

func TestDictionariesHaveSameKeysAndPlaceholders(t *testing.T) {
	read := func(lang Language) map[string]string {
		t.Helper()
		b, err := JSON(lang)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		if token, err := dec.Token(); err != nil || token != json.Delim('{') {
			t.Fatalf("字典不是对象：%v", err)
		}
		dict := map[string]string{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				t.Fatal(err)
			}
			name := key.(string)
			if _, exists := dict[name]; exists {
				t.Fatalf("字典键重复：%s", name)
			}
			var value string
			if err := dec.Decode(&value); err != nil || strings.TrimSpace(value) == "" {
				t.Fatalf("文案为空或不是文字：%s，%v", name, err)
			}
			dict[name] = value
		}
		dec.Token()
		if _, err := dec.Token(); err != io.EOF {
			t.Fatal("字典后面还有内容")
		}
		return dict
	}
	zh := read(ZhCN)
	for _, lang := range []Language{En, Ja} {
		other := read(lang)
		if len(zh) != len(other) {
			t.Errorf("字典键数量不同：%s=%d，中文=%d", lang, len(other), len(zh))
		}
		for key, text := range zh {
			translation, exists := other[key]
			if !exists {
				t.Errorf("%s缺文案：%s", lang, key)
				continue
			}
			names := func(s string) []string {
				all := placeholder.FindAllString(s, -1)
				sort.Strings(all)
				return all
			}
			if !reflect.DeepEqual(names(text), names(translation)) {
				t.Errorf("占位符不同：%s/%s", lang, key)
			}
			if strings.HasSuffix(key, "_one") {
				if _, exists := zh[strings.TrimSuffix(key, "_one")+"_other"]; !exists {
					t.Errorf("缺少复数形式：%s", key)
				}
			}
			if strings.HasSuffix(key, "_other") {
				if _, exists := zh[strings.TrimSuffix(key, "_other")+"_one"]; !exists {
					t.Errorf("缺少单数形式：%s", key)
				}
			}
		}
	}
}

func TestSelectLanguage(t *testing.T) {
	for _, tc := range []struct {
		preference, system string
		want               Language
	}{
		{"en", "zh-CN", En}, {"zh-CN", "en-US", ZhCN},
		{"system", "en-US", En}, {"system", "zh-TW", ZhCN},
		{"system", "en_GB.UTF-8", En}, {"system", "zh_CN.UTF-8@variant", ZhCN},
		{"system", "fr-FR", En}, {"system", "de-DE", En},
		{"system", "???", En}, {"system", "C.UTF-8", En}, {"system", "", En},
		{"system", "zh-CN;q=0.2,en;q=0.9", En}, {"system", "en;q=0,zh-CN;q=1", ZhCN},
		{"ja", "zh-CN,en", Ja}, {"en", "ja-JP", En},
		{"system", "ja-JP", Ja}, {"system", "JA-jp", Ja},
		{"system", "ja_JP.UTF-8", Ja}, {"system", "ja_JP.UTF-8@variant", Ja},
		{"system", "ja;q=0.9,en;q=0.8,zh-CN;q=0.7", Ja},
		{"system", "ja;q=0.3,en;q=0.8", En}, {"system", "ja;q=0,zh;q=0.8", ZhCN},
		{"system", "ja;q=0.5,zh;q=0.5", Ja}, {"system", "zh;q=0.5,ja;q=0.5", ZhCN},
		{"system", "fr;q=1,ja;q=0.5", Ja}, {"system", "fr;q=0.9,zh;q=0.5", ZhCN},
		{"system", "ja;q=bad,en;q=0.8", En}, {"system", "en;q=2,ja;q=0.8", Ja},
		{"system", "en;q=NaN,ja;q=0.8", Ja}, {"system", "ja;q=0,en;q=0", En},
		{"system", "ko-KR,fr-FR;q=0.9", En},
	} {
		if got := Select(tc.preference, tc.system); got != tc.want {
			t.Errorf("Select(%q,%q)=%q，期望%q", tc.preference, tc.system, got, tc.want)
		}
	}
}

func TestFormatUsesSinglePassAndCountForms(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  string
	}{{1, "minute"}, {2, "minutes"}} {
		if got, err := Format(En, Message{Code: "units.minutes", Args: map[string]any{"count": tc.count}}); err != nil || got != tc.want {
			t.Fatalf("单位的数量形式错误：%q，%v", got, err)
		}
	}
	got, err := Format(En, Message{Code: "settings.unknown_field", Args: map[string]any{"field": "{count}"}})
	if err != nil || got != "The field {count} does not exist." {
		t.Fatalf("参数中的花括号被重新解释：%q，%v", got, err)
	}
	for _, tc := range []struct {
		count any
		want  string
	}{
		{1, "1 channel selected"}, {int64(1), "1 channel selected"},
		{float64(1), "1 channel selected"}, {json.Number("1"), "1 channel selected"},
		{0, "0 channels selected"}, {2, "2 channels selected"},
		{-1, "-1 channels selected"}, {1.5, "1.5 channels selected"},
	} {
		got, err := Format(En, Message{Code: "settings.selected_channels", Args: map[string]any{"count": tc.count}})
		if err != nil || got != tc.want {
			t.Errorf("数量%v：%q，%v", tc.count, got, err)
		}
	}
	for _, count := range []any{nil, "1", math.NaN(), math.Inf(1), json.Number("NaN")} {
		if got, err := Format(En, Message{Code: "settings.selected_channels", Args: map[string]any{"count": count}}); err == nil || got != "" {
			t.Errorf("无效数量通过：%v，%q", count, got)
		}
	}
	for _, m := range []Message{{Code: "missing"}, {Code: "settings.unknown_field"}, {Code: "settings.unknown_field", Args: map[string]any{"field": nil}}} {
		if got, err := Format(En, m); err == nil || got != "" {
			t.Errorf("未知代码或缺参仍显示模板：%+v，%q", m, got)
		}
	}
	if _, err := Format("fr", Message{Code: "common.cancel"}); err == nil {
		t.Fatal("格式化接受了不支持的语言")
	}
}

func TestDictionaryBytesAreIndependent(t *testing.T) {
	for _, lang := range []Language{ZhCN, En, Ja} {
		first, _ := JSON(lang)
		original := bytes.Clone(first)
		first[0] = '!'
		second, _ := JSON(lang)
		if !bytes.Equal(second, original) {
			t.Fatal("修改返回字节污染了字典")
		}
	}
	if _, err := JSON("system"); err == nil {
		t.Fatal("读取字典前必须确定实际语言")
	}
}
