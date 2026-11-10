package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultTimeout = 10 * time.Second
const maxResponseBytes = 1 << 20

type httpReply struct {
	Status     int
	Body       []byte
	ReadFailed bool
}

func failure(code string) Result     { return Result{ErrorCode: code} }
func configError(field string) error { return &ConfigError{Field: field} }

func failureFor(err error) Result {
	if errors.Is(err, context.Canceled) {
		return failure("cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure("timeout")
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return failure("timeout")
	}
	return failure("transport")
}

func decodeObject(raw json.RawMessage, target any, field string) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return configError(field)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return configError(field)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return configError(field)
	}
	return nil
}

func validHTTPURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || !(strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) || u.Hostname() == "" || u.Fragment != "" || strings.ContainsAny(value, "\r\n") {
		return false
	}
	_, err = url.ParseQuery(u.RawQuery)
	return err == nil
}

func (s *Sender) sendJSON(ctx context.Context, address string, payload any, headers http.Header, timeout time.Duration) (httpReply, Result) {
	body, err := json.Marshal(payload)
	if err != nil {
		return httpReply{}, failure("invalid_message")
	}
	if headers == nil {
		headers = make(http.Header)
	} else {
		headers = headers.Clone()
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json; charset=utf-8")
	}
	return s.sendRequest(ctx, http.MethodPost, address, body, headers, timeout)
}

func (s *Sender) sendRequest(ctx context.Context, method, address string, body []byte, headers http.Header, timeout time.Duration) (httpReply, Result) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, method, address, bytes.NewReader(body))
	if err != nil {
		return httpReply{}, failure("invalid_config")
	}
	req.Header = headers.Clone()
	if host := req.Header.Get("Host"); host != "" {
		req.Host = host
		req.Header.Del("Host")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return httpReply{}, failureFor(err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	reply := httpReply{Status: resp.StatusCode, Body: data, ReadFailed: readErr != nil || len(data) > maxResponseBytes}
	result := Result{OK: resp.StatusCode >= 200 && resp.StatusCode < 300, HTTPStatus: resp.StatusCode}
	if !result.OK {
		result.ErrorCode = "http_status"
	}
	return reply, result
}

func codeResult(reply httpReply, field string, expected int64, optional bool) Result {
	result := Result{OK: true, HTTPStatus: reply.Status}
	if reply.ReadFailed {
		result.OK = false
		result.ErrorCode = "invalid_response"
		return result
	}
	if len(bytes.TrimSpace(reply.Body)) == 0 && optional {
		return result
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(reply.Body, &object) != nil || object == nil {
		result.OK = false
		result.ErrorCode = "invalid_response"
		return result
	}
	value, exists := object[field]
	if !exists && optional {
		return result
	}
	var code int64
	if !exists || json.Unmarshal(value, &code) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		result.OK = false
		result.ErrorCode = "invalid_response"
		return result
	}
	result.ProviderCode = strconv.FormatInt(code, 10)
	if code != expected {
		result.OK = false
		result.ErrorCode = "provider_rejected"
	}
	return result
}
