package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
)

func parseProxyURL(raw string) (*url.URL, error) {
	address, err := url.Parse(raw)
	if err != nil || address == nil || address.Opaque != "" || address.RawQuery != "" || address.Fragment != "" || address.ForceQuery ||
		(address.Scheme != "http" && address.Scheme != "socks5") || (address.Path != "" && address.Path != "/") || strings.ContainsAny(raw, "\r\n") {
		return nil, accountError(ErrorInvalid, "proxies.invalid_url", "url")
	}
	host, port, err := net.SplitHostPort(address.Host)
	if err != nil || host == "" || strings.ContainsAny(host, "\r\n\t ") {
		return nil, accountError(ErrorInvalid, "proxies.invalid_url", "url")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, accountError(ErrorInvalid, "proxies.invalid_url", "url")
	}
	if address.Scheme == "socks5" && address.User != nil {
		password, _ := address.User.Password()
		if len(address.User.Username()) > 255 || len(password) > 255 {
			return nil, accountError(ErrorInvalid, "proxies.invalid_url", "url")
		}
	}
	return address, nil
}

func parseProxyPatch(patch map[string]json.RawMessage, previous *ProxyView) (string, *url.URL, error) {
	var name string
	if previous != nil {
		name = previous.Name
	}
	var address *url.URL
	for key, raw := range patch {
		if !utf8.Valid(raw) {
			return "", nil, accountError(ErrorInvalid, "proxies.invalid", key)
		}
		switch key {
		case "name":
			var value *string
			if json.Unmarshal(raw, &value) != nil || value == nil || strings.TrimSpace(*value) == "" {
				return "", nil, accountError(ErrorInvalid, "proxies.invalid", key)
			}
			name = strings.TrimSpace(*value)
		case "url":
			var value *string
			if json.Unmarshal(raw, &value) != nil || value == nil {
				return "", nil, accountError(ErrorInvalid, "proxies.invalid_url", key)
			}
			var err error
			address, err = parseProxyURL(*value)
			if err != nil {
				return "", nil, err
			}
		default:
			return "", nil, accountError(ErrorInvalid, "proxies.invalid", key)
		}
	}
	if name == "" {
		return "", nil, accountError(ErrorInvalid, "proxies.invalid", "name")
	}
	if previous == nil && address == nil {
		return "", nil, accountError(ErrorInvalid, "proxies.invalid_url", "url")
	}
	return name, address, nil
}

func newProxyUUID() string {
	var bytes [16]byte
	rand.Read(bytes[:])
	bytes[6], bytes[8] = bytes[6]&0x0f|0x40, bytes[8]&0x3f|0x80
	text := hex.EncodeToString(bytes[:])
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

func loadProxyURLTx(ctx context.Context, tx *sql.Tx, cipher *security.Cipher, id int64) (*url.URL, error) {
	var uuid string
	var encrypted []byte
	if err := tx.QueryRowContext(ctx, `SELECT uuid,url_enc FROM proxies WHERE id=?`, id).Scan(&uuid, &encrypted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, accountError(ErrorNotFound, "proxies.not_found", "")
		}
		return nil, err
	}
	if cipher == nil {
		return nil, security.ErrMasterKey
	}
	plain, err := cipher.Open(encrypted, "proxies", "url_enc", uuid)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return parseProxyURL(string(plain))
}

func loadProxyViewTx(ctx context.Context, tx *sql.Tx, id int64) (ProxyView, error) {
	view := ProxyView{Configured: true}
	err := tx.QueryRowContext(ctx, `SELECT id,name FROM proxies WHERE id=?`, id).Scan(&view.ID, &view.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyView{}, accountError(ErrorNotFound, "proxies.not_found", "")
	}
	return view, err
}

func checkProxyNameTx(ctx context.Context, tx *sql.Tx, id int64, name string) error {
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM proxies WHERE name=? AND id<>?)`, name, id).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return accountError(ErrorInvalid, "proxies.name_in_use", "name")
	}
	return nil
}

func (s *Service) ListProxies(ctx context.Context, query ListQuery) (Page[ProxyView], error) {
	query, err := normalizeListQuery(query)
	if err != nil {
		return Page[ProxyView]{}, err
	}
	var anchor struct {
		ID int64 `json:"id"`
	}
	if err := decodeCursor(query.Cursor, "proxies", struct{}{}, &anchor); err != nil {
		return Page[ProxyView]{}, err
	}
	if query.Cursor != "" && anchor.ID <= 0 {
		return Page[ProxyView]{}, queryError()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page[ProxyView]{}, storageFailure(err)
	}
	defer tx.Rollback()
	page := Page[ProxyView]{Items: []ProxyView{}}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxies`).Scan(&page.Total); err != nil {
		return Page[ProxyView]{}, storageFailure(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name FROM proxies WHERE id>? ORDER BY id LIMIT ?`, anchor.ID, query.Limit+1)
	if err != nil {
		return Page[ProxyView]{}, storageFailure(err)
	}
	for rows.Next() {
		view := ProxyView{Configured: true}
		if err := rows.Scan(&view.ID, &view.Name); err != nil {
			rows.Close()
			return Page[ProxyView]{}, storageFailure(err)
		}
		page.Items = append(page.Items, view)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Page[ProxyView]{}, storageFailure(err)
	}
	if err := rows.Close(); err != nil {
		return Page[ProxyView]{}, storageFailure(err)
	}
	if len(page.Items) > int(query.Limit) {
		page.Items = page.Items[:query.Limit]
		anchor.ID = page.Items[len(page.Items)-1].ID
		next, err := encodeCursor("proxies", struct{}{}, anchor)
		if err != nil {
			return Page[ProxyView]{}, err
		}
		page.NextCursor = &next
	}
	if err := tx.Commit(); err != nil {
		return Page[ProxyView]{}, storageFailure(err)
	}
	return page, nil
}

func (s *Service) CreateProxy(ctx context.Context, patch map[string]json.RawMessage) (ProxyView, error) {
	finish, err := s.beginMutation()
	if err != nil {
		return ProxyView{}, err
	}
	defer finish()
	name, address, err := parseProxyPatch(patch, nil)
	if err != nil {
		return ProxyView{}, err
	}
	if s.cipher == nil {
		return ProxyView{}, storageFailure(security.ErrMasterKey)
	}
	uuid := newProxyUUID()
	encrypted, err := s.cipher.Seal([]byte(address.String()), "proxies", "url_enc", uuid)
	if err != nil {
		return ProxyView{}, storageFailure(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProxyView{}, storageFailure(err)
	}
	defer tx.Rollback()
	if err := checkProxyNameTx(ctx, tx, 0, name); err != nil {
		return ProxyView{}, storageFailure(err)
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return ProxyView{}, storageFailure(err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO proxies(uuid,name,url_enc,created_at) VALUES(?,?,?,?)`, uuid, name, encrypted, now.UnixMilli())
	if err != nil {
		return ProxyView{}, storageFailure(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return ProxyView{}, storageFailure(err)
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return ProxyView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return ProxyView{}, storageFailure(err)
	}
	_ = s.emit("proxy.changed", map[string]any{"proxy_id": id})
	return ProxyView{ID: id, Name: name, Configured: true}, nil
}

func (s *Service) UpdateProxy(ctx context.Context, id int64, patch map[string]json.RawMessage) (ProxyView, error) {
	finish, err := s.beginMutation()
	if err != nil {
		return ProxyView{}, err
	}
	defer finish()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProxyView{}, storageFailure(err)
	}
	defer tx.Rollback()
	view, err := loadProxyViewTx(ctx, tx, id)
	if err != nil {
		return ProxyView{}, storageFailure(err)
	}
	name, address, err := parseProxyPatch(patch, &view)
	if err != nil {
		return ProxyView{}, err
	}
	if err := checkProxyNameTx(ctx, tx, id, name); err != nil {
		return ProxyView{}, storageFailure(err)
	}
	if address != nil {
		if s.cipher == nil {
			return ProxyView{}, storageFailure(security.ErrMasterKey)
		}
		var uuid string
		if err := tx.QueryRowContext(ctx, `SELECT uuid FROM proxies WHERE id=?`, id).Scan(&uuid); err != nil {
			return ProxyView{}, storageFailure(err)
		}
		encrypted, err := s.cipher.Seal([]byte(address.String()), "proxies", "url_enc", uuid)
		if err != nil {
			return ProxyView{}, storageFailure(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxies SET name=?,url_enc=? WHERE id=?`, name, encrypted, id); err != nil {
			return ProxyView{}, storageFailure(err)
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE proxies SET name=? WHERE id=?`, name, id); err != nil {
		return ProxyView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return ProxyView{}, storageFailure(err)
	}
	view.Name = name
	_ = s.emit("proxy.changed", map[string]any{"proxy_id": id})
	return view, nil
}

func (s *Service) DeleteProxy(ctx context.Context, id int64) error {
	finish, err := s.beginMutation()
	if err != nil {
		return err
	}
	defer finish()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	if _, err := loadProxyViewTx(ctx, tx, id); err != nil {
		return storageFailure(err)
	}
	var inUse bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE proxy_id=?)`, id).Scan(&inUse); err != nil {
		return storageFailure(err)
	}
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	var aiProxy *int64
	if err := json.Unmarshal(snapshot.Values["ai.proxy_id"], &aiProxy); err != nil {
		return storageFailure(err)
	}
	if inUse || aiProxy != nil && *aiProxy == id {
		return accountError(ErrorConflict, "proxies.in_use", "")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM proxies WHERE id=?`, id); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	_ = s.emit("proxy.changed", map[string]any{"proxy_id": id})
	return nil
}

func (s *Service) TestProxy(ctx context.Context, id int64) (ProxyTestResult, error) {
	finish, err := s.beginMutation()
	if err != nil {
		return ProxyTestResult{}, err
	}
	defer finish()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProxyTestResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	address, err := loadProxyURLTx(ctx, tx, s.cipher, id)
	if err != nil {
		return ProxyTestResult{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return ProxyTestResult{}, storageFailure(err)
	}
	request, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(request, "tcp", address.Host)
	if ctx.Err() != nil {
		if conn != nil {
			conn.Close()
		}
		return ProxyTestResult{}, ctx.Err()
	}
	if err != nil {
		return ProxyTestResult{Reason: &locales.Message{Code: "proxies.test_failed"}}, nil
	}
	conn.Close()
	return ProxyTestResult{OK: true}, nil
}
