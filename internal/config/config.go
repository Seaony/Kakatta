// Package config 读取启动参数并准备数据目录和主密钥。
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

type Lookup func(string) (string, bool)

type Config struct {
	DataDir     string
	Listen      string
	Environment settings.Environment
}

type Paths struct {
	DataDir, Database, Key, Browser, Images, Backups string
}

func Load(args []string, lookup Lookup) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var cfg Config
	flags := flag.NewFlagSet("kakatta", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.DataDir, "data", "./data", "数据目录")
	flags.StringVar(&cfg.Listen, "listen", ":8080", "监听地址")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 || cfg.DataDir == "" {
		return Config{}, errors.New("启动参数无效")
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil || port == "" {
		return Config{}, errors.New("监听地址必须包含端口")
	}
	if cfg.DataDir, err = filepath.Abs(cfg.DataDir); err != nil {
		return Config{}, errors.New("无法确定数据目录")
	}
	cfg.Environment.TZ, _ = lookup("TZ")
	if proxies, exists := lookup("KAKATTA_TRUSTED_PROXIES"); exists {
		cfg.Environment.TrustedProxies = &proxies
	}
	return cfg, nil
}

func CreateDataDirectories(dataDir string) (Paths, error) {
	p := Paths{
		DataDir: dataDir, Database: filepath.Join(dataDir, "kakatta.db"),
		Key: filepath.Join(dataDir, "secret.key"), Browser: filepath.Join(dataDir, "browser"),
		Images: filepath.Join(dataDir, "cache", "images"), Backups: filepath.Join(dataDir, "backups"),
	}
	for _, path := range []string{p.DataDir, p.Browser, filepath.Join(dataDir, "cache"), p.Images, p.Backups} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return Paths{}, fmt.Errorf("无法创建数据目录：%w", err)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Paths{}, errors.New("数据路径必须是真实目录")
		}
	}
	return p, nil
}

func LoadMasterKey(paths Paths, lookup Lookup) ([]byte, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	encoded, inlineSet := lookup("KAKATTA_SECRET_KEY")
	keyPath, fileSet := lookup("KAKATTA_SECRET_KEY_FILE")
	if inlineSet && fileSet {
		return nil, errors.New("KAKATTA_SECRET_KEY 和 KAKATTA_SECRET_KEY_FILE 只能设其中一个")
	}
	if inlineSet {
		key, err := security.DecodeMasterKey(encoded)
		if err != nil {
			return nil, errors.New("KAKATTA_SECRET_KEY 必须是 32 字节主密钥的标准 Base64")
		}
		return key, nil
	}
	if fileSet {
		if keyPath == "" {
			return nil, errors.New("KAKATTA_SECRET_KEY_FILE 不能为空")
		}
		return readKey(keyPath)
	}
	if info, err := os.Lstat(paths.Key); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return nil, errors.New("默认主密钥必须是权限为 0600 的普通文件")
		}
		return readKey(paths.Key)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("无法读取默认主密钥")
	}
	if _, err := os.Lstat(paths.Database); err == nil {
		return nil, errors.New("数据库已存在但主密钥缺失，请恢复原主密钥")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("无法检查已有数据库")
	}
	key := security.NewMasterKey()
	file, err := os.CreateTemp(paths.DataDir, ".secret-key-*")
	if err != nil {
		clear(key)
		return nil, errors.New("无法创建主密钥文件")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(key)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		clear(key)
		return nil, errors.New("主密钥文件写入失败，请检查数据目录")
	}
	// 完整写好才公开文件名，并发启动也不能覆盖已有密钥。
	if err := os.Link(file.Name(), paths.Key); err != nil {
		clear(key)
		if errors.Is(err, os.ErrExist) {
			return LoadMasterKey(paths, lookup)
		}
		return nil, errors.New("无法保存主密钥文件")
	}
	directory, err := os.Open(paths.DataDir)
	if err != nil {
		clear(key)
		return nil, errors.New("无法同步主密钥目录")
	}
	syncErr := directory.Sync()
	closeErr = directory.Close()
	if syncErr != nil || closeErr != nil {
		clear(key)
		return nil, errors.New("主密钥目录同步失败")
	}
	return key, nil
}

func readKey(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("无法读取指定的主密钥文件")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("主密钥必须是普通文件")
	}
	key, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, errors.New("主密钥文件必须恰好包含 32 字节")
	}
	return key, nil
}
