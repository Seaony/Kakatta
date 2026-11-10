package update

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Seaony/Kakatta/internal/locales"
)

const cacheKey = "update.check.v1"

type cacheState struct {
	Version       int              `json:"version"`
	LastAttemptAt *int64           `json:"last_attempt_at"`
	LastSuccessAt *int64           `json:"last_success_at"`
	RetryAt       *int64           `json:"retry_at"`
	KnownRelease  *Release         `json:"known_release"`
	ETag          string           `json:"etag"`
	LastModified  string           `json:"last_modified"`
	Error         *locales.Message `json:"error"`
}

func (state cacheState) nextCheckAt(now int64) int64 {
	if state.LastAttemptAt == nil {
		return now
	}
	next := *state.LastAttemptAt + checkInterval.Milliseconds()
	if state.RetryAt != nil {
		next = max(next, *state.RetryAt)
	}
	return next
}

func (state cacheState) validate() error {
	if state.Version != 1 {
		return errors.New("更新检查缓存版本无效")
	}
	for _, at := range []*int64{state.LastAttemptAt, state.LastSuccessAt, state.RetryAt} {
		if at != nil && *at <= 0 {
			return errors.New("更新检查时间无效")
		}
	}
	if state.LastAttemptAt == nil && (state.LastSuccessAt != nil || state.RetryAt != nil || state.Error != nil) || (state.LastSuccessAt == nil) != (state.KnownRelease == nil) || state.KnownRelease != nil && !validRelease(*state.KnownRelease) {
		return errors.New("更新检查缓存不完整")
	}
	if state.KnownRelease == nil && (state.ETag != "" || state.LastModified != "") || state.ETag != "" && !validETag(state.ETag) {
		return errors.New("更新检查条件标记无效")
	}
	if state.LastModified != "" {
		if _, err := http.ParseTime(state.LastModified); err != nil {
			return errors.New("更新检查修改时间无效")
		}
	}
	if state.Error != nil && (state.Error.Code != "update.check_failed" || state.Error.Args == nil || len(state.Error.Args) != 0) {
		return errors.New("更新检查错误标记无效")
	}
	return nil
}

func readCacheTx(ctx context.Context, tx *sql.Tx) (cacheState, error) {
	state := cacheState{Version: 1}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, cacheKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, nil
		}
		return cacheState{}, err
	}
	state = cacheState{}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return cacheState{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return cacheState{}, errors.New("更新检查缓存包含多余内容")
	}
	return state, state.validate()
}

func encodeCache(state cacheState) (string, error) {
	if err := state.validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(state)
	return string(raw), err
}
