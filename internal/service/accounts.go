package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
)

const accountColumns = `id,name,proxy_id,nickname,avatar_url,state,state_reason,state_changed_at,cooldown_until,verify_level,verify_url,last_cooldown_end_at,cooldown_manual_used,last_run_ended_at,next_gap_seconds,last_used_at,created_at`
const accountCleanupPrefix = "account.cleanup."
const accountLoggedOutPrefix = "account.logged_out."
const maxAccountID int64 = 12767

type accountScanner interface{ Scan(...any) error }

func scanAccount(row accountScanner) (Account, error) {
	var account Account
	var reason *string
	err := row.Scan(&account.ID, &account.Name, &account.ProxyID, &account.Nickname, &account.AvatarURL, &account.State,
		&reason, &account.StateChangedAt, &account.CooldownUntil, &account.VerifyLevel, &account.VerifyURL,
		&account.LastCooldownEndAt, &account.CooldownManualUsed, &account.LastRunEndedAt, &account.NextGapSeconds,
		&account.LastUsedAt, &account.CreatedAt)
	if err != nil {
		return Account{}, err
	}
	if reason != nil {
		account.StateReason, err = logMessage(sql.NullString{String: *reason, Valid: true})
		if err != nil {
			return Account{}, err
		}
	}
	return account, nil
}

func loadAccountTx(ctx context.Context, tx *sql.Tx, id int64) (Account, error) {
	account, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, accountError(ErrorNotFound, "accounts.not_found", "")
	}
	return account, err
}

func pickAccountTx(ctx context.Context, tx *sql.Tx) (*int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT a.id FROM accounts a LEFT JOIN tasks t ON t.account_id=a.id
		WHERE a.state='active' GROUP BY a.id ORDER BY COUNT(t.id),a.created_at,a.id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func assignUnboundTasksTx(ctx context.Context, tx *sql.Tx, nowMS int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE account_id IS NULL AND account_pinned=0 ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	assigned := []int64{}
	for _, taskID := range ids {
		accountID, err := pickAccountTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		if accountID == nil {
			break
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET account_id=?,next_run_at=?,updated_at=? WHERE id=? AND account_id IS NULL AND account_pinned=0`, *accountID, nowMS, nowMS, taskID); err != nil {
			return nil, err
		}
		assigned = append(assigned, taskID)
	}
	return assigned, nil
}

func reassignAutomaticTasksTx(ctx context.Context, tx *sql.Tx, fromAccountID, nowMS int64) ([]int64, error) {
	changed, err := boundAccountTaskIDsTx(ctx, tx, fromAccountID, false)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET account_id=NULL,next_run_at=?,updated_at=? WHERE account_id=? AND account_pinned=0`, nowMS, nowMS, fromAccountID); err != nil {
		return nil, err
	}
	assigned, err := assignUnboundTasksTx(ctx, tx, nowMS)
	return changedAccountTaskIDs(changed, assigned), err
}

func boundAccountTaskIDsTx(ctx context.Context, tx *sql.Tx, accountID int64, includePinned bool) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE account_id=? AND (account_pinned=0 OR ?) ORDER BY id`, accountID, includePinned)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func changedAccountTaskIDs(first, second []int64) []int64 {
	seen := map[int64]bool{}
	ids := []int64{}
	for _, group := range [][]int64{first, second} {
		for _, id := range group {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func accountError(kind ErrorKind, code, field string) error {
	reason := locales.Message{Code: code, Args: map[string]any{}}
	problem := &ServiceError{Kind: kind, Reason: reason}
	if field != "" {
		reason.Args["field"] = field
		problem.Fields = []settings.Issue{{Key: field, Reason: reason}}
	}
	return problem
}

func parseAccountPatch(patch map[string]json.RawMessage, previous *Account) (string, *int64, error) {
	var name string
	var proxyID *int64
	if previous != nil {
		name, proxyID = previous.Name, previous.ProxyID
	}
	for key, raw := range patch {
		if !utf8.Valid(raw) {
			return "", nil, accountError(ErrorInvalid, "accounts.invalid", key)
		}
		switch key {
		case "name":
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &name) != nil || !utf8.ValidString(name) || strings.TrimSpace(name) == "" {
				return "", nil, accountError(ErrorInvalid, "accounts.invalid", key)
			}
			name = strings.TrimSpace(name)
		case "proxy_id":
			if json.Unmarshal(raw, &proxyID) != nil || proxyID != nil && (*proxyID <= 0 || *proxyID > limits.MaxSafeInteger) {
				return "", nil, accountError(ErrorInvalid, "accounts.invalid", key)
			}
		default:
			return "", nil, accountError(ErrorInvalid, "accounts.invalid", key)
		}
	}
	if name == "" {
		return "", nil, accountError(ErrorInvalid, "accounts.invalid", "name")
	}
	return name, proxyID, nil
}

func checkAccountProxyTx(ctx context.Context, tx *sql.Tx, proxyID *int64) error {
	if proxyID == nil {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM proxies WHERE id=?)`, *proxyID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return accountError(ErrorInvalid, "accounts.invalid", "proxy_id")
	}
	return nil
}

func (s *Service) accountViewTx(ctx context.Context, tx *sql.Tx, account Account) (AccountView, error) {
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return AccountView{}, err
	}
	clock, err := readQuotaClockTx(ctx, tx)
	if err != nil {
		return AccountView{}, err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return AccountView{}, err
	}
	observed := time.Now()
	if s.now != nil {
		observed = s.now()
	}
	clock, now, err := quota.AdvanceClock(clock, observed, timezone)
	if err != nil {
		return AccountView{}, err
	}
	limits, err := quotaLimits(snapshot)
	if err != nil {
		return AccountView{}, err
	}
	view, _, err := loadQuotaTx(ctx, tx, account.ID, clock, now, limits)
	if err != nil {
		return AccountView{}, err
	}
	return AccountView{Account: account, Quota: view}, nil
}

func (s *Service) GetAccount(ctx context.Context, id int64) (AccountView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	defer tx.Rollback()
	account, err := loadAccountTx(ctx, tx, id)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	view, err := s.accountViewTx(ctx, tx, account)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return AccountView{}, storageFailure(err)
	}
	view.Session = s.sessionSnapshot(id)
	return view, nil
}

func (s *Service) ListAccounts(ctx context.Context, query ListQuery) (Page[AccountView], error) {
	query, err := normalizeListQuery(query)
	if err != nil {
		return Page[AccountView]{}, err
	}
	var anchor struct {
		ID int64 `json:"id"`
	}
	if err := decodeCursor(query.Cursor, "accounts", struct{}{}, &anchor); err != nil {
		return Page[AccountView]{}, err
	}
	if query.Cursor != "" && anchor.ID <= 0 {
		return Page[AccountView]{}, queryError()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page[AccountView]{}, storageFailure(err)
	}
	defer tx.Rollback()
	page := Page[AccountView]{Items: []AccountView{}}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&page.Total); err != nil {
		return Page[AccountView]{}, storageFailure(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id>? ORDER BY id LIMIT ?`, anchor.ID, query.Limit+1)
	if err != nil {
		return Page[AccountView]{}, storageFailure(err)
	}
	accounts := []Account{}
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			rows.Close()
			return Page[AccountView]{}, storageFailure(err)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Page[AccountView]{}, storageFailure(err)
	}
	if err := rows.Close(); err != nil {
		return Page[AccountView]{}, storageFailure(err)
	}
	if len(accounts) > int(query.Limit) {
		accounts = accounts[:query.Limit]
		anchor.ID = accounts[len(accounts)-1].ID
		next, err := encodeCursor("accounts", struct{}{}, anchor)
		if err != nil {
			return Page[AccountView]{}, err
		}
		page.NextCursor = &next
	}
	for _, account := range accounts {
		view, err := s.accountViewTx(ctx, tx, account)
		if err != nil {
			return Page[AccountView]{}, storageFailure(err)
		}
		page.Items = append(page.Items, view)
	}
	if err := tx.Commit(); err != nil {
		return Page[AccountView]{}, storageFailure(err)
	}
	for i := range page.Items {
		page.Items[i].Session = s.sessionSnapshot(page.Items[i].ID)
	}
	return page, nil
}

func (s *Service) CreateAccount(ctx context.Context, patch map[string]json.RawMessage) (AccountView, error) {
	finish, err := s.beginMutation()
	if err != nil {
		return AccountView{}, err
	}
	defer finish()
	name, proxyID, err := parseAccountPatch(patch, nil)
	if err != nil {
		return AccountView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	defer tx.Rollback()
	if err := checkAccountProxyTx(ctx, tx, proxyID); err != nil {
		return AccountView{}, storageFailure(err)
	}
	var lastID int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='accounts'),0)`).Scan(&lastID); err != nil {
		return AccountView{}, storageFailure(err)
	}
	if lastID < 0 || lastID >= maxAccountID {
		return AccountView{}, accountError(ErrorInvalid, "accounts.id_capacity", "")
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO accounts(name,proxy_id,state,state_changed_at,created_at) VALUES(?,?,'signed_out',?,?)`, name, proxyID, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	id, err := result.LastInsertId()
	if err != nil || id < 1 || id > maxAccountID {
		return AccountView{}, storageFailure(fmt.Errorf("账号编号分配失败"))
	}
	account, err := loadAccountTx(ctx, tx, id)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return AccountView{}, storageFailure(err)
	}
	view, err := s.accountViewTx(ctx, tx, account)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return AccountView{}, storageFailure(err)
	}
	_ = s.emit("account.changed", map[string]any{"account_id": id})
	s.wakeScheduler()
	return view, nil
}

func (s *Service) UpdateAccount(ctx context.Context, id int64, patch map[string]json.RawMessage) (AccountView, error) {
	finish, err := s.beginMutation()
	if err != nil {
		return AccountView{}, err
	}
	defer finish()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	defer tx.Rollback()
	account, err := loadAccountTx(ctx, tx, id)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	name, proxyID, err := parseAccountPatch(patch, &account)
	if err != nil {
		return AccountView{}, err
	}
	if err := checkAccountProxyTx(ctx, tx, proxyID); err != nil {
		return AccountView{}, storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET name=?,proxy_id=? WHERE id=?`, name, proxyID, id); err != nil {
		return AccountView{}, storageFailure(err)
	}
	account.Name, account.ProxyID = name, proxyID
	view, err := s.accountViewTx(ctx, tx, account)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return AccountView{}, storageFailure(err)
	}
	view.Session = s.sessionSnapshot(id)
	_ = s.emit("account.changed", map[string]any{"account_id": id})
	s.wakeScheduler()
	return view, nil
}

func accountCleanupPendingTx(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meta WHERE key=?)`, accountCleanupPrefix+strconv.FormatInt(id, 10)).Scan(&pending)
	return pending, err
}

func accountLoggedOutTx(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, accountLoggedOutPrefix+strconv.FormatInt(id, 10)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if value != "1" {
		return false, errors.New("账号退出标记损坏")
	}
	return true, nil
}

func setAccountLoggedOutTx(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value='1'`, accountLoggedOutPrefix+strconv.FormatInt(id, 10))
	return err
}

func clearAccountLoggedOutTx(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, accountLoggedOutPrefix+strconv.FormatInt(id, 10))
	return err
}

func (s *Service) removeAccountProfile(ctx context.Context, id int64) error {
	if id < 1 || id > maxAccountID {
		return accountError(ErrorUnavailable, "accounts.cleanup_failed", "")
	}
	if err := os.RemoveAll(filepath.Join(s.dataDir, "browser", strconv.FormatInt(id, 10))); err != nil {
		return accountError(ErrorUnavailable, "accounts.cleanup_failed", "")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, accountCleanupPrefix+strconv.FormatInt(id, 10)); err != nil {
		return storageFailure(err)
	}
	return nil
}

func (s *Service) retryAccountCleanup(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM meta WHERE key LIKE ? ORDER BY key`, accountCleanupPrefix+"%")
	if err != nil {
		return storageFailure(err)
	}
	ids := []int64{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return storageFailure(err)
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(key, accountCleanupPrefix), 10, 64)
		if err != nil || id < 1 || id > maxAccountID || value != "1" || key != accountCleanupPrefix+strconv.FormatInt(id, 10) {
			rows.Close()
			return accountError(ErrorUnavailable, "accounts.cleanup_failed", "")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return storageFailure(err)
	}
	if err := rows.Close(); err != nil {
		return storageFailure(err)
	}
	for _, id := range ids {
		if err := s.removeAccountProfile(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) DeleteAccount(ctx context.Context, id int64) error {
	if id < 1 || id > maxAccountID {
		return accountError(ErrorNotFound, "accounts.not_found", "")
	}
	resume, err := s.quiesceAccount(ctx, id)
	if err != nil {
		return err
	}
	defer resume()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	_, loadErr := loadAccountTx(ctx, tx, id)
	if loadErr != nil {
		var problem *ServiceError
		if !errors.As(loadErr, &problem) || problem.Kind != ErrorNotFound {
			return storageFailure(loadErr)
		}
		pending, err := accountCleanupPendingTx(ctx, tx, id)
		if err != nil {
			return storageFailure(err)
		}
		if !pending {
			return loadErr
		}
		if err := tx.Commit(); err != nil {
			return storageFailure(err)
		}
		return s.removeAccountProfile(ctx, id)
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value='1'`, accountCleanupPrefix+strconv.FormatInt(id, 10)); err != nil {
		return storageFailure(err)
	}
	bound, err := boundAccountTaskIDsTx(ctx, tx, id, true)
	if err != nil {
		return storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET account_pinned=0 WHERE account_id=?`, id); err != nil {
		return storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id); err != nil {
		return storageFailure(err)
	}
	if err := clearAccountLoggedOutTx(ctx, tx, id); err != nil {
		return storageFailure(err)
	}
	changed, err := assignUnboundTasksTx(ctx, tx, now.UnixMilli())
	if err != nil {
		return storageFailure(err)
	}
	eventsChanged := false
	for _, kind := range []string{"account_verify", "account_login", "account_deny"} {
		resolved, err := accountEvents(now.UnixMilli()).ResolveTx(ctx, tx, kind, id)
		if err != nil {
			return storageFailure(err)
		}
		eventsChanged = eventsChanged || resolved
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	changed = changedAccountTaskIDs(bound, changed)
	_ = s.emit("account.changed", map[string]any{"account_id": id})
	if eventsChanged {
		_ = s.emit("system_event.changed", map[string]any{"all": true})
	}
	if len(changed) > 0 {
		_ = s.emit("task.changed", map[string]any{"task_ids": changed})
	}
	s.wakeScheduler()
	return s.removeAccountProfile(ctx, id)
}

func (s *Service) LogoutAccount(ctx context.Context, id int64) (AccountView, error) {
	if id < 1 || id > maxAccountID {
		return AccountView{}, accountError(ErrorNotFound, "accounts.not_found", "")
	}
	resume, err := s.quiesceAccount(ctx, id)
	if err != nil {
		return AccountView{}, err
	}
	defer resume()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	defer tx.Rollback()
	account, err := loadAccountTx(ctx, tx, id)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value='1'`, accountCleanupPrefix+strconv.FormatInt(id, 10)); err != nil {
		return AccountView{}, storageFailure(err)
	}
	_, eventsChanged, err := signOutAccountTx(ctx, tx, id, now.UnixMilli())
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	account, err = loadAccountTx(ctx, tx, id)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	changed, err := reassignAutomaticTasksTx(ctx, tx, id, now.UnixMilli())
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return AccountView{}, storageFailure(err)
	}
	view, err := s.accountViewTx(ctx, tx, account)
	if err != nil {
		return AccountView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return AccountView{}, storageFailure(err)
	}
	_ = s.emit("account.changed", map[string]any{"account_id": id})
	if eventsChanged {
		_ = s.emit("system_event.changed", map[string]any{"all": true})
	}
	if len(changed) > 0 {
		_ = s.emit("task.changed", map[string]any{"task_ids": changed})
	}
	s.wakeScheduler()
	if err := s.removeAccountProfile(ctx, id); err != nil {
		return AccountView{}, err
	}
	return view, nil
}
