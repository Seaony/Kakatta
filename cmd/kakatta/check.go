package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/preflight"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

func checkEnvironment(ctx context.Context, args []string, output io.Writer, lookup config.Lookup) (result error) {
	cfg, err := config.Load(args, lookup)
	if err != nil {
		return err
	}
	timezone, proxies, language, err := checkSavedConfiguration(ctx, cfg, lookup)
	zone := timezone
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	logDir := filepath.Join(cfg.DataDir, "diagnostics", "preflight")
	log, logErr := diagnostics.Open(logDir, zone)
	if logErr != nil {
		return errors.Join(err, logErr)
	}
	defer func() { result = errors.Join(result, log.Close()) }()
	if err != nil {
		check := preflight.Check{ID: "settings", Detail: err.Error(), Action: preflightText(language, "settings_remedy")}
		return errors.Join(errors.New(preflightText(language, "failed_no_access")), json.NewEncoder(output).Encode(check),
			log.Event("preflight", map[string]any{"check": check}), log.Summary([]preflight.Check{check}))
	}
	checks := preflight.Run(ctx, preflight.Options{DataDir: cfg.DataDir, LogDir: logDir, Timezone: timezone, Proxies: proxies, Language: language})
	passed := true
	for _, check := range checks {
		if err := json.NewEncoder(output).Encode(check); err != nil {
			return err
		}
		if err := log.Event("preflight", map[string]any{"check": check}); err != nil {
			return err
		}
		passed = passed && check.Passed
	}
	if err := log.Summary(checks); err != nil {
		return err
	}
	if !passed {
		return errors.New(preflightText(language, "failed"))
	}
	return nil
}

// 自检只读已有配置，不初始化数据库、不迁移格式、不修改额度。
func checkSavedConfiguration(ctx context.Context, cfg config.Config, lookup config.Lookup) (string, []*url.URL, locales.Language, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	system, _ := lookup("LANG")
	language := locales.Select("system", system)
	stored := settings.Values{}
	path := filepath.Join(cfg.DataDir, "kakatta.db")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		snapshot, err := settings.Resolve(stored, cfg.Environment)
		if err != nil {
			return "", nil, language, errors.New(preflightText(language, "saved_defaults_invalid"))
		}
		var timezone string
		err = json.Unmarshal(snapshot.Values["general.timezone"], &timezone)
		return timezone, nil, language, err
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, language, errors.New(preflightText(language, "saved_database_unreadable"))
	}
	address := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", address.String())
	if err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_database_open_failed"))
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT key,value_json FROM settings`)
	if err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_settings_read_failed"))
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return "", nil, language, errors.New(preflightText(language, "saved_settings_unreadable"))
		}
		stored[key] = json.RawMessage(value)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_settings_unreadable"))
	}
	snapshot, err := settings.Resolve(stored, cfg.Environment)
	if err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_settings_invalid"))
	}
	var preference string
	if err := json.Unmarshal(snapshot.Values["general.language"], &preference); err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_settings_invalid"))
	}
	language = locales.Select(preference, system)
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_timezone_unreadable"))
	}
	rows, err = db.QueryContext(ctx, `SELECT uuid,url_enc FROM proxies ORDER BY id`)
	if err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_proxies_read_failed"))
	}
	defer rows.Close()
	var proxies []*url.URL
	var cipher *security.Cipher
	for rows.Next() {
		var id string
		var encrypted []byte
		if err := rows.Scan(&id, &encrypted); err != nil {
			return "", nil, language, errors.New(preflightText(language, "saved_proxies_read_failed"))
		}
		if cipher == nil {
			key, err := config.LoadMasterKey(config.Paths{DataDir: cfg.DataDir, Database: path, Key: filepath.Join(cfg.DataDir, "secret.key")}, lookup)
			if err != nil {
				return "", nil, language, errors.New(preflightText(language, "saved_key_required"))
			}
			cipher, err = security.NewCipher(key)
			clear(key)
			if err != nil {
				return "", nil, language, errors.New(preflightText(language, "saved_key_invalid"))
			}
		}
		plain, err := cipher.Open(encrypted, "proxies", "url_enc", id)
		if err != nil {
			return "", nil, language, errors.New(preflightText(language, "saved_proxy_decrypt_failed"))
		}
		proxy, err := url.Parse(string(plain))
		clear(plain)
		if err != nil || proxy.Hostname() == "" || proxy.Scheme != "http" && proxy.Scheme != "socks5" {
			return "", nil, language, errors.New(preflightText(language, "saved_proxy_invalid"))
		}
		proxies = append(proxies, proxy)
	}
	if err := rows.Err(); err != nil {
		return "", nil, language, errors.New(preflightText(language, "saved_proxies_incomplete"))
	}
	return timezone, proxies, language, nil
}

func preflightText(language locales.Language, code string) string {
	text, _ := locales.Format(language, locales.Message{Code: "preflight." + code})
	return text
}
