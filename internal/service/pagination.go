package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/Seaony/Kakatta/internal/locales"
)

const maxCursorBytes = 4096

type cursorEnvelope struct {
	Version int             `json:"version"`
	Scope   string          `json:"scope"`
	Query   string          `json:"query"`
	Anchor  json.RawMessage `json:"anchor"`
}

func queryError() error {
	return &ServiceError{Kind: ErrorBadRequest, Reason: locales.Message{Code: "service.invalid_query", Args: map[string]any{}}}
}

func normalizeListQuery(q ListQuery) (ListQuery, error) {
	if q.Limit < 0 || q.Limit > 200 || len(q.Cursor) > maxCursorBytes {
		return ListQuery{}, queryError()
	}
	if q.Limit == 0 {
		q.Limit = 50
	}
	return q, nil
}

func cursorQuery(query any) (string, error) {
	raw, err := json.Marshal(query)
	if err != nil {
		return "", queryError()
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// 游标只保存排序锚点，筛选仍由服务重新校验并生成 SQL。
func encodeCursor(scope string, query any, anchor any) (string, error) {
	if scope == "" {
		return "", queryError()
	}
	digest, err := cursorQuery(query)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(anchor)
	if err != nil || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return "", queryError()
	}
	raw, err := json.Marshal(cursorEnvelope{Version: 1, Scope: scope, Query: digest, Anchor: data})
	if err != nil {
		return "", queryError()
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if len(token) > maxCursorBytes {
		return "", queryError()
	}
	return token, nil
}

func decodeCursor(token, scope string, query any, anchor any) error {
	if token == "" {
		return nil
	}
	if len(token) > maxCursorBytes || scope == "" {
		return queryError()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return queryError()
	}
	var value cursorEnvelope
	if strictCursorJSON(raw, &value) != nil || value.Version != 1 || value.Scope != scope || !bytes.HasPrefix(bytes.TrimSpace(value.Anchor), []byte("{")) {
		return queryError()
	}
	digest, err := cursorQuery(query)
	if err != nil || value.Query != digest {
		return queryError()
	}
	if strictCursorJSON(value.Anchor, anchor) != nil {
		return queryError()
	}
	return nil
}

func strictCursorJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return queryError()
	}
	return nil
}
