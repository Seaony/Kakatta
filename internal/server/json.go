package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

const maxBody = 1 << 20

type ErrorEnvelope struct {
	Error     locales.Message  `json:"error"`
	Fields    []settings.Issue `json:"fields"`
	RequestID string           `json:"request_id"`
}

func message(code string) locales.Message { return locales.Message{Code: code} }

func WriteJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data, _ = json.Marshal(ErrorEnvelope{Error: message("http.internal"), Fields: []settings.Issue{}, RequestID: RequestID(r.Context())})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead && status != http.StatusNoContent {
		_, _ = w.Write(append(data, '\n'))
	}
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, reason locales.Message, fields []settings.Issue) {
	if fields == nil {
		fields = []settings.Issue{}
	}
	WriteJSON(w, r, status, ErrorEnvelope{Error: reason, Fields: fields, RequestID: RequestID(r.Context())})
}

type requestError struct {
	status int
	code   string
	fields []settings.Issue
}

func (e *requestError) Error() string { return e.code }

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	data, err := readJSONObject(w, r)
	if err != nil {
		return err
	}
	if !exactFields(data, target) {
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	return nil
}

func readJSONObject(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if len(r.Header.Values("Content-Type")) != 1 {
		return nil, &requestError{status: http.StatusUnsupportedMediaType, code: "http.unsupported_media_type"}
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, &requestError{status: http.StatusUnsupportedMediaType, code: "http.unsupported_media_type"}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &requestError{status: http.StatusRequestEntityTooLarge, code: "http.body_too_large"}
		}
		return nil, &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	if !utf8.Valid(data) || !uniqueJSONObject(data) {
		return nil, &requestError{status: http.StatusBadRequest, code: "http.invalid_json"}
	}
	return data, nil
}

func exactFields(data []byte, target any) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil {
		return false
	}
	typeOf := reflect.TypeOf(target).Elem()
	allowed := map[string]bool{}
	for i := 0; i < typeOf.NumField(); i++ {
		name, _, _ := strings.Cut(typeOf.Field(i).Tag.Get("json"), ",")
		allowed[name] = true
	}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func uniqueJSONObject(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') || !uniqueObject(decoder, 1) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func uniqueObject(decoder *json.Decoder, depth int) bool {
	if depth > 100 {
		return false
	}
	keys := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || keys[key] {
			return false
		}
		keys[key] = true
		if !uniqueValue(decoder, depth) {
			return false
		}
	}
	token, err := decoder.Token()
	return err == nil && token == json.Delim('}')
}

func uniqueValue(decoder *json.Decoder, depth int) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			return uniqueObject(decoder, depth+1)
		case '[':
			if depth >= 100 {
				return false
			}
			for decoder.More() {
				if !uniqueValue(decoder, depth+1) {
					return false
				}
			}
			token, err = decoder.Token()
			return err == nil && token == json.Delim(']')
		default:
			return false
		}
	}
	return true
}

func required(values map[string]string) error {
	var fields []settings.Issue
	for key, value := range values {
		if value == "" {
			fields = append(fields, settings.Issue{Key: key, Reason: message("http.invalid_request")})
		}
	}
	if len(fields) != 0 {
		sort.Slice(fields, func(i, j int) bool { return fields[i].Key < fields[j].Key })
		return &requestError{status: http.StatusBadRequest, code: "http.invalid_request", fields: fields}
	}
	return nil
}
