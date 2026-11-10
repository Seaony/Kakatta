package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"golang.org/x/mod/semver"
)

const checkInterval = 24 * time.Hour

type Checker struct {
	deps     Dependencies
	client   *http.Client
	inFlight chan struct{}
}

var _ API = (*Checker)(nil)

func New(deps Dependencies) (*Checker, error) {
	if deps.DB == nil || deps.Settings == nil || deps.HTTP == nil || deps.HTTP.Transport == nil || strings.TrimSpace(deps.CurrentVersion) == "" || !utf8.ValidString(deps.CurrentVersion) || strings.ContainsAny(deps.CurrentVersion, "\r\n\x00") {
		return nil, errors.New("更新检查依赖不完整或无效")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	client := *deps.HTTP
	client.Timeout, client.Jar = 10*time.Second, nil
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 3 || request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.URL.User != nil {
			return errors.New("更新检查重定向无效")
		}
		return nil
	}
	return &Checker{deps: deps, client: &client, inFlight: make(chan struct{}, 1)}, nil
}

func (c *Checker) Read(ctx context.Context) (Status, error) {
	tx, err := c.deps.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Status{}, storageFailure(err)
	}
	defer tx.Rollback()
	enabled, state, err := c.readTx(ctx, tx)
	if err != nil {
		return Status{}, storageFailure(err)
	}
	now := c.deps.Now().UTC().UnixMilli()
	result := Status{CurrentVersion: c.deps.CurrentVersion, Enabled: enabled, LastAttemptAt: state.LastAttemptAt,
		LastSuccessAt: state.LastSuccessAt, KnownRelease: state.KnownRelease, Error: state.Error}
	result.Stale = state.LastSuccessAt == nil || state.Error != nil || state.LastAttemptAt != nil && *state.LastAttemptAt > *state.LastSuccessAt || state.LastSuccessAt != nil && now >= *state.LastSuccessAt+checkInterval.Milliseconds()
	if enabled {
		next := state.nextCheckAt(now)
		result.NextCheckAt = &next
		if current := fullVersion(c.deps.CurrentVersion); !result.Stale && current != "" && state.KnownRelease != nil {
			available := semver.Compare(fullVersion(state.KnownRelease.Version), current) > 0
			result.UpdateAvailable = &available
		}
	}
	return result, storageFailure(tx.Commit())
}

func (c *Checker) readTx(ctx context.Context, tx *sql.Tx) (bool, cacheState, error) {
	snapshot, err := c.deps.Settings.SnapshotTx(ctx, tx)
	if err != nil {
		return false, cacheState{}, err
	}
	var enabled bool
	if err := json.Unmarshal(snapshot.Values["general.update_check"], &enabled); err != nil {
		return false, cacheState{}, err
	}
	state, err := readCacheTx(ctx, tx)
	return enabled, state, err
}

func (c *Checker) CheckDue(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.inFlight <- struct{}{}:
		defer func() { <-c.inFlight }()
	default:
		return nil
	}
	tx, err := c.deps.DB.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	enabled, state, err := c.readTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	now := c.deps.Now().UTC().UnixMilli()
	if !enabled || now < state.nextCheckAt(now) {
		return nil
	}
	state.LastAttemptAt, state.RetryAt = &now, nil
	claimed, err := encodeCache(state)
	if err != nil {
		return storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cacheKey, claimed); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}

	// 尝试先落库，再释放连接访问网络；重启不能把当天的尝试抹掉。
	outcome, requestErr := c.fetch(ctx, state)
	if err := ctx.Err(); err != nil {
		return err
	}
	state.RetryAt = outcome.retryAt
	if requestErr != nil {
		state.Error = &locales.Message{Code: "update.check_failed", Args: map[string]any{}}
	} else {
		finished := max(now, c.deps.Now().UTC().UnixMilli())
		state.LastSuccessAt, state.Error = &finished, nil
		state.KnownRelease, state.ETag, state.LastModified = outcome.release, outcome.etag, outcome.lastModified
	}
	if err := c.complete(ctx, claimed, state); err != nil {
		return storageFailure(err)
	}
	if requestErr != nil {
		return unavailable("update.check_failed", requestErr)
	}
	return nil
}

func (c *Checker) complete(ctx context.Context, claimed string, state cacheState) error {
	raw, err := encodeCache(state)
	if err != nil {
		return err
	}
	tx, err := c.deps.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key=? AND value=?`, raw, cacheKey, claimed)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("更新检查记录已被替换")
	}
	return tx.Commit()
}

type operationError struct {
	business *service.ServiceError
	cause    error
}

func (e *operationError) Error() string   { return e.business.Error() }
func (e *operationError) Unwrap() []error { return []error{e.business, e.cause} }

func unavailable(code string, cause error) error {
	return &operationError{business: &service.ServiceError{Kind: service.ErrorUnavailable,
		Reason: locales.Message{Code: code, Args: map[string]any{}}, Fields: []settings.Issue{}}, cause: cause}
}

func storageFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return unavailable("service.storage_failure", err)
}
