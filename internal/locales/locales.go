// Package locales 共用服务端和网页端的文案。
package locales

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"

	"golang.org/x/text/language"
)

type Language string

const (
	ZhCN Language = "zh-CN"
	En   Language = "en"
)

type Message struct {
	Code string         `json:"code"`
	Args map[string]any `json:"args"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if m.Args == nil {
		m.Args = map[string]any{}
	}
	return json.Marshal(plain(m))
}

//go:embed zh-CN.json en.json
var files embed.FS

var dictionaries = load()
var placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var matcher = language.NewMatcher([]language.Tag{language.SimplifiedChinese, language.English})

func load() map[Language]map[string]string {
	all := make(map[Language]map[string]string, 2)
	for _, lang := range []Language{ZhCN, En} {
		b, err := files.ReadFile(string(lang) + ".json")
		if err != nil {
			panic(err)
		}
		var dict map[string]string
		if err := json.Unmarshal(b, &dict); err != nil {
			panic(err)
		}
		all[lang] = dict
	}
	return all
}

func Select(preference, systemLocale string) Language {
	if preference == string(En) || preference == string(ZhCN) {
		return Language(preference)
	}
	// 容器的 LANG 可能带字符集和修饰符，浏览器则传 Accept-Language。
	if !strings.ContainsAny(systemLocale, ",;") {
		systemLocale, _, _ = strings.Cut(systemLocale, ".")
		systemLocale, _, _ = strings.Cut(systemLocale, "@")
		systemLocale = strings.ReplaceAll(systemLocale, "_", "-")
	}
	_, index := language.MatchStrings(matcher, systemLocale)
	if index == 1 {
		return En
	}
	return ZhCN
}

func Format(lang Language, m Message) (string, error) {
	dict, ok := dictionaries[lang]
	if !ok {
		return "", errors.New("locales: unsupported language")
	}
	code := m.Code
	if _, plural := dict[code+"_other"]; plural {
		count, ok := number(m.Args["count"])
		if !ok {
			return "", errors.New("locales: count must be a finite number")
		}
		code += "_other"
		if count == 1 {
			code = m.Code + "_one"
		}
	}
	template, ok := dict[code]
	if !ok {
		return "", fmt.Errorf("locales: unknown message code %q", m.Code)
	}
	var missing bool
	// 只走一次原模板，参数中的花括号不能再次触发替换。
	out := placeholder.ReplaceAllStringFunc(template, func(token string) string {
		arg, found := m.Args[token[1:len(token)-1]]
		if !found || arg == nil {
			missing = true
			return ""
		}
		return fmt.Sprint(arg)
	})
	if missing {
		return "", errors.New("locales: missing message argument")
	}
	return out, nil
}

func number(v any) (float64, bool) {
	if n, ok := v.(json.Number); ok {
		f, err := n.Float64()
		return f, err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	}
	r := reflect.ValueOf(v)
	var f float64
	switch r.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f = float64(r.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f = float64(r.Uint())
	case reflect.Float32, reflect.Float64:
		f = r.Float()
	default:
		return 0, false
	}
	return f, !math.IsInf(f, 0) && !math.IsNaN(f)
}

func JSON(lang Language) ([]byte, error) {
	if _, ok := dictionaries[lang]; !ok {
		return nil, errors.New("locales: unsupported language")
	}
	b, err := files.ReadFile(string(lang) + ".json")
	return bytes.Clone(b), err
}
