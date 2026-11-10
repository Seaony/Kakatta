package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

// 分块保留未结束的字符；encoding/json 本身会把坏 UTF-8 换成替代字符。
type taskImportReader struct {
	ctx     context.Context
	reader  io.Reader
	tail    []byte
	failure error
}

func (r *taskImportReader) Read(dst []byte) (int, error) {
	if r.failure != nil {
		return 0, r.failure
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(dst)
	data := append(r.tail, dst[:n]...)
	r.tail = nil
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			r.tail = bytes.Clone(data)
			break
		}
		v, size := utf8.DecodeRune(data)
		if v == utf8.RuneError && size == 1 {
			r.failure = transferInvalidFile()
			return n, r.failure
		}
		data = data[size:]
	}
	if err == io.EOF && len(r.tail) > 0 {
		r.failure = transferInvalidFile()
		return n, r.failure
	}
	if err != nil && err != io.EOF {
		r.failure = err
	}
	return n, err
}

func scanTaskImport(ctx context.Context, src io.Reader, visit func(int64, json.RawMessage) error) (int64, error) {
	reader := &taskImportReader{ctx: ctx, reader: src}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return 0, taskImportReadError(ctx, err)
	}
	if token != json.Delim('{') {
		return 0, transferInvalidFile()
	}
	seen := map[string]bool{}
	var total int64
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return total, taskImportReadError(ctx, err)
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return total, transferInvalidFile()
		}
		seen[key] = true
		switch key {
		case "tasks":
			token, err = decoder.Token()
			if err != nil {
				return total, taskImportReadError(ctx, err)
			}
			if token != json.Delim('[') {
				return total, transferInvalidFile()
			}
			for decoder.More() {
				var raw json.RawMessage
				if err = decoder.Decode(&raw); err != nil {
					return total, taskImportReadError(ctx, err)
				}
				total++
				if err = ctx.Err(); err != nil {
					return total, err
				}
				if visit != nil {
					if err = visit(total, raw); err != nil {
						return total, err
					}
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return total, taskImportReadError(ctx, err)
			}
		case "format", "version", "exported_at":
			var raw json.RawMessage
			if err = decoder.Decode(&raw); err != nil {
				return total, taskImportReadError(ctx, err)
			}
			if key == "format" {
				var value string
				if json.Unmarshal(raw, &value) != nil || value != "kakatta.tasks" {
					return total, transferInvalidFile()
				}
			}
			if key == "version" {
				version, e := strconv.ParseInt(string(raw), 10, 64)
				if e != nil || version != 1 {
					return total, &ServiceError{Kind: ErrorBadRequest, Reason: locales.Message{Code: "tasks.import_unsupported_version", Args: map[string]any{}}, Fields: []settings.Issue{}}
				}
			}
			if key == "exported_at" {
				if reason := taskJSONShape(raw, reflect.TypeFor[string]()); reason != nil {
					return total, transferInvalidFile()
				}
			}
		default:
			return total, transferInvalidFile()
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return total, taskImportReadError(ctx, err)
	}
	if !seen["format"] || !seen["version"] || !seen["tasks"] {
		return total, transferInvalidFile()
	}
	var extra any
	err = decoder.Decode(&extra)
	if err != io.EOF {
		if err == nil {
			return total, transferInvalidFile()
		}
		return total, taskImportReadError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return total, err
	}
	if reader.failure != nil {
		return total, taskImportReadError(ctx, reader.failure)
	}
	return total, nil
}
func taskImportReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return transferInvalidFile()
	}
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	var business *ServiceError
	if errors.As(err, &syntax) || errors.As(err, &shape) || errors.As(err, &business) {
		return transferInvalidFile()
	}
	return storageFailure(err)
}

func decodeImportedTask(raw json.RawMessage, corrections *[]settings.Issue) (TaskPatch, *taskImportOptions, error) {
	fields, reason := taskJSONObject(bytes.TrimSpace(raw))
	if reason != nil {
		return nil, nil, invalidTaskField("task", *reason)
	}
	patch := TaskPatch{}
	options := &taskImportOptions{}
	model := reflect.TypeFor[TaskFields]()
	known := taskJSONFields(model)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := fields[key]
		switch key {
		case "account_id", "account_pinned", "channel_ids", "region_province", "region_city", "region_district":
			importCorrection(corrections, key, "unknown_field")
			continue
		case "region":
			t := reflect.TypeFor[struct {
				Province *string `json:"province"`
				City     *string `json:"city"`
				District *string `json:"district"`
			}]()
			clean, err := cleanImportedJSON(value, t, key, corrections)
			if err != nil {
				return nil, nil, err
			}
			var region map[string]json.RawMessage
			json.Unmarshal(clean, &region)
			for name, item := range region {
				patch["region_"+name] = item
			}
			continue
		case "channels":
			clean, err := cleanImportedJSON(value, reflect.TypeFor[[]TaskChannelRef](), key, corrections)
			if err != nil {
				return nil, nil, err
			}
			refs := []TaskChannelRef{}
			json.Unmarshal(clean, &refs)
			options.Channels = &refs
			continue
		case "min_price", "max_price", "budget_min", "budget_max":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				patch[key] = value
				continue
			}
			cents, err := parseTaskImportMoney(value)
			if err != nil {
				return nil, nil, invalidTaskField(key, *taskFieldReason(err.Error()))
			}
			patch[key] = json.RawMessage(strconv.FormatInt(cents, 10))
			continue
		}
		index, ok := known[key]
		if !ok {
			importCorrection(corrections, key, "unknown_field")
			continue
		}
		clean, err := cleanImportedJSON(value, model.Field(index).Type, key, corrections)
		if err != nil {
			return nil, nil, err
		}
		patch[key] = clean
	}
	return patch, options, nil
}

func cleanImportedJSON(raw json.RawMessage, t reflect.Type, path string, corrections *[]settings.Issue) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	bad := func(reason *locales.Message) (json.RawMessage, error) { return nil, invalidTaskField(path, *reason) }
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(raw, []byte("null")) {
			return raw, nil
		}
		return cleanImportedJSON(raw, t.Elem(), path, corrections)
	}
	if t.Kind() == reflect.Struct {
		values, reason := taskJSONObject(raw)
		if reason != nil {
			return bad(reason)
		}
		known := taskJSONFields(t)
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			index, ok := known[key]
			if !ok {
				delete(values, key)
				importCorrection(corrections, path+"."+key, "unknown_field")
				continue
			}
			value, err := cleanImportedJSON(values[key], t.Field(index).Type, path+"."+key, corrections)
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return json.Marshal(values)
	}
	if t.Kind() == reflect.Slice {
		if len(raw) == 0 || raw[0] != '[' {
			return bad(taskFieldReason("list_required"))
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return bad(taskFieldReason("invalid_json"))
		}
		for index, item := range items {
			clean, err := cleanImportedJSON(item, t.Elem(), path+"["+strconv.Itoa(index)+"]", corrections)
			if err != nil {
				return nil, err
			}
			items[index] = clean
		}
		return json.Marshal(items)
	}
	if reason := taskJSONShape(raw, t); reason != nil {
		return bad(reason)
	}
	return raw, nil
}

func parseTaskImportMoney(raw json.RawMessage) (int64, error) {
	value := string(bytes.TrimSpace(raw))
	bad := func(code string) (int64, error) { return 0, errors.New(code) }
	if value == "" || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) || !json.Valid(raw) {
		return bad("number_required")
	}
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	exponent := int64(0)
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		exp := value[index+1:]
		value = value[:index]
		neg := strings.HasPrefix(exp, "-")
		exp = strings.TrimLeft(exp, "+-")
		exp = strings.TrimLeft(exp, "0")
		bound := int64(len(value)) + 100
		boundText := strconv.FormatInt(bound, 10)
		if len(exp) > len(boundText) || len(exp) == len(boundText) && exp > boundText {
			exponent = bound
		} else if exp != "" {
			exponent, _ = strconv.ParseInt(exp, 10, 64)
		}
		if neg {
			exponent = -exponent
		}
	}
	decimals := 0
	if index := strings.IndexByte(value, '.'); index >= 0 {
		decimals = len(value) - index - 1
		value = value[:index] + value[index+1:]
	}
	digits := strings.TrimLeft(value, "0")
	if digits == "" {
		return 0, nil
	}
	if negative {
		return bad("nonnegative_required")
	}
	shift := exponent + 2 - int64(decimals)
	if shift < 0 {
		cut := -shift
		if cut >= int64(len(digits)) {
			return bad("whole_cents_required")
		}
		end := len(digits) - int(cut)
		if strings.Trim(digits[end:], "0") != "" {
			return bad("whole_cents_required")
		}
		digits = digits[:end]
	} else {
		if int64(len(digits))+shift > 16 {
			return bad("overflow")
		}
		digits += strings.Repeat("0", int(shift))
	}
	if len(digits) > 16 || len(digits) == 16 && digits > strconv.FormatInt(limits.MaxSafeInteger, 10) {
		return bad("overflow")
	}
	result, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return bad("overflow")
	}
	return result, nil
}
