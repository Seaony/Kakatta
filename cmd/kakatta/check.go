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
	"github.com/Seaony/Kakatta/internal/preflight"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

func checkEnvironment(ctx context.Context, args []string, output io.Writer, lookup config.Lookup) (result error) {
	cfg, err := config.Load(args, lookup)
	if err != nil {
		return err
	}
	timezone, proxies, err := checkSavedConfiguration(ctx, cfg, lookup)
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
		check := preflight.Check{ID: "settings", Detail: err.Error(), Action: "请检查数据目录和主密钥配置；已有数据请恢复原文件，不要删除账号保护记录"}
		return errors.Join(errors.New("环境自检失败，未打开浏览器或访问闲鱼"), json.NewEncoder(output).Encode(check),
			log.Event("preflight", map[string]any{"check": check}), log.Summary([]preflight.Check{check}))
	}
	checks := preflight.Run(ctx, preflight.Options{DataDir: cfg.DataDir, LogDir: logDir, Timezone: timezone, Proxies: proxies})
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
		return errors.New("环境自检未全部通过，请按各项处理办法修好后重试")
	}
	return nil
}

// 自检只读已有配置，不初始化数据库、不迁移格式、不修改额度。
func checkSavedConfiguration(ctx context.Context, cfg config.Config, lookup config.Lookup) (string, []*url.URL, error) {
	stored := settings.Values{}
	path := filepath.Join(cfg.DataDir, "kakatta.db")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		snapshot, err := settings.Resolve(stored, cfg.Environment)
		if err != nil {
			return "", nil, errors.New("默认设置或时区无效")
		}
		var timezone string
		err = json.Unmarshal(snapshot.Values["general.timezone"], &timezone)
		return timezone, nil, err
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("无法读取现有数据库")
	}
	address := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", address.String())
	if err != nil {
		return "", nil, errors.New("无法只读打开现有数据库")
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT key,value_json FROM settings`)
	if err != nil {
		return "", nil, errors.New("无法读取已有设置，请先检查数据库版本和完整性")
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return "", nil, errors.New("已有设置无法读取")
		}
		stored[key] = json.RawMessage(value)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return "", nil, errors.New("已有设置无法读取")
	}
	snapshot, err := settings.Resolve(stored, cfg.Environment)
	if err != nil {
		return "", nil, errors.New("已有设置或时区无效")
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return "", nil, errors.New("已有时区无法读取")
	}
	rows, err = db.QueryContext(ctx, `SELECT uuid,url_enc FROM proxies ORDER BY id`)
	if err != nil {
		return "", nil, errors.New("无法读取已有代理设置")
	}
	defer rows.Close()
	var proxies []*url.URL
	var cipher *security.Cipher
	for rows.Next() {
		var id string
		var encrypted []byte
		if err := rows.Scan(&id, &encrypted); err != nil {
			return "", nil, errors.New("无法读取已有代理设置")
		}
		if cipher == nil {
			key, err := config.LoadMasterKey(config.Paths{DataDir: cfg.DataDir, Database: path, Key: filepath.Join(cfg.DataDir, "secret.key")}, lookup)
			if err != nil {
				return "", nil, errors.New("代理设置需要原主密钥，请检查主密钥配置")
			}
			cipher, err = security.NewCipher(key)
			clear(key)
			if err != nil {
				return "", nil, errors.New("无法使用原主密钥")
			}
		}
		plain, err := cipher.Open(encrypted, "proxies", "url_enc", id)
		if err != nil {
			return "", nil, errors.New("无法解密代理设置，请检查主密钥")
		}
		proxy, err := url.Parse(string(plain))
		clear(plain)
		if err != nil || proxy.Hostname() == "" || proxy.Scheme != "http" && proxy.Scheme != "socks5" {
			return "", nil, errors.New("已有代理地址格式无效")
		}
		proxies = append(proxies, proxy)
	}
	if err := rows.Err(); err != nil {
		return "", nil, errors.New("无法读完已有代理设置")
	}
	return timezone, proxies, nil
}
