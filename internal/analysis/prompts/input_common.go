package prompts

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

func inputError(field string) error {
	return &analysis.Error{Message: locales.Message{Code: "analysis.prompts.invalid_input", Args: map[string]any{"field": field}}}
}

func userJSON(value any) (string, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil || !utf8.Valid(encoded.Bytes()) {
		return "", inputError("json")
	}
	// 用 Number 保留原来的十进制；RawMessage 和结构体也要按嵌套键名排序。
	decoder := json.NewDecoder(&encoded)
	decoder.UseNumber()
	canonical, err := readUserJSONValue(decoder)
	if err != nil {
		return "", inputError("json")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", inputError("json")
	}
	encoded.Reset()
	if err := encoder.Encode(canonical); err != nil {
		return "", inputError("json")
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}

func readUserJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if _, exists := object[name]; !ok || exists {
				return nil, inputError("json")
			}
			value, err := readUserJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		if close, err := decoder.Token(); err != nil || close != json.Delim('}') {
			return nil, inputError("json")
		}
		return object, nil
	case json.Delim('['):
		array := []any{}
		for decoder.More() {
			value, err := readUserJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if close, err := decoder.Token(); err != nil || close != json.Delim(']') {
			return nil, inputError("json")
		}
		return array, nil
	default:
		return token, nil
	}
}

func centsNumber(cents int64) json.Number {
	return decimalCentsNumber(strconv.FormatInt(cents, 10))
}

func fractionalCentsNumber(cents float64) (json.Number, error) {
	if math.IsNaN(cents) || math.IsInf(cents, 0) {
		return "", inputError("cents")
	}
	return decimalCentsNumber(strconv.FormatFloat(cents, 'f', -1, 64)), nil
}

func decimalCentsNumber(text string) json.Number {
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	integer, fraction, _ := strings.Cut(text, ".")
	digits := integer + fraction
	point := len(integer) - 2
	var result string
	if point <= 0 {
		result = "0." + strings.Repeat("0", -point) + digits
	} else {
		result = digits[:point] + "." + digits[point:]
	}
	result = strings.TrimSuffix(strings.TrimRight(result, "0"), ".")
	if negative && result != "0" {
		result = "-" + result
	}
	return json.Number(result)
}

func cloneProfile(profile analysis.Profile) analysis.Profile {
	profile.Specs = slices.Clone(profile.Specs)
	for i := range profile.Specs {
		profile.Specs[i].Options = slices.Clone(profile.Specs[i].Options)
		profile.Specs[i].Values = slices.Clone(profile.Specs[i].Values)
	}
	profile.Musts = slices.Clone(profile.Musts)
	profile.Bonuses = slices.Clone(profile.Bonuses)
	profile.Risks = slices.Clone(profile.Risks)
	profile.AllowedKinds = slices.Clone(profile.AllowedKinds)
	return profile
}

func copyTaskID(id *int64) *int64 {
	if id == nil {
		return nil
	}
	copy := *id
	return &copy
}
