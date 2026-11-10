package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stderr, os.LookupEnv); err != nil {
		slog.Error("程序停止", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer, lookup config.Lookup) error {
	if len(args) > 0 {
		switch args[0] {
		case "reset-password", "disable-totp":
			return maintenance(ctx, args[0], args[1:], input, output)
		case "help", "--help", "-h":
			_, err := fmt.Fprintln(output, "用法：kakatta [--data ./data] [--listen :8080]\n恢复密码：kakatta reset-password [--data ./data] [--disable-totp]\n关闭两步验证：kakatta disable-totp [--data ./data]")
			return err
		}
	}
	cfg, err := config.Load(args, lookup)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	return serve(ctx, cfg, lookup, slog.New(slog.NewJSONHandler(output, nil)))
}

func serve(ctx context.Context, cfg config.Config, lookup config.Lookup, logger *slog.Logger) (result error) {
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(cfg.DataDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("数据路径必须是真实目录")
	}
	serverLock, err := config.AcquireServerLock(cfg.DataDir)
	if err != nil {
		return err
	}
	defer serverLock.Close()
	restoreLock, err := config.AcquireRestoreLock(cfg.DataDir, true)
	if err != nil {
		return err
	}
	defer restoreLock.Close()
	keyConfig := readRuntimeKeyConfig(lookup)
	if keyConfig.fileSet {
		if err := checkExternalKeyPaths(keyConfig.file, cfg.DataDir, ".restore", backup.WorkDirName); err != nil {
			return err
		}
	}
	pendingCleanup, err := recoverRestoreFiles(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	if pendingCleanup {
		logger.Error("恢复已提交，旧副本等待清理", "reason", "restore.storage_failure")
	}
	if err := backup.CleanupInterrupted(cfg.DataDir); err != nil {
		return err
	}
	paths, err := config.CreateDataDirectories(cfg.DataDir)
	if err != nil {
		return err
	}
	key, source, keyFile, err := keyConfig.load(paths)
	if err != nil {
		return err
	}
	defer clear(key[:])
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	runtime, err := newRuntimeManager(ctx, cfg, paths, key, source, keyFile, logger)
	clear(key[:])
	if err != nil {
		return err
	}
	if err := restoreLock.Close(); err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		return errors.Join(err, runtime.Close(cleanup))
	}
	httpServer := &http.Server{
		Handler: runtime, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute,
		MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return ctx },
	}
	bootstrap := runtime.graph.bootstrap
	if bootstrap.Token != "" {
		logger.Info("首次初始化令牌，只显示这一次", "token", bootstrap.Token, "expires_at", bootstrap.ExpiresAt)
	}
	logger.Info("服务已启动", "listen", listener.Addr().String())
	finished := make(chan error, 1)
	go func() { finished <- httpServer.Serve(listener) }()
	ended := false
	select {
	case result = <-finished:
		ended = true
		if errors.Is(result, http.ErrServerClosed) {
			result = nil
		}
	case result = <-runtime.failed:
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	result = errors.Join(result, runtime.Close(shutdown))
	if err := httpServer.Shutdown(shutdown); err != nil {
		_ = httpServer.Close()
		result = errors.Join(result, err)
	}
	if !ended {
		<-finished
	}
	return result
}

type runtimeKeyConfig struct {
	inline, file       string
	inlineSet, fileSet bool
}

func readRuntimeKeyConfig(lookup config.Lookup) runtimeKeyConfig {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	inline, inlineSet := lookup("KAKATTA_SECRET_KEY")
	file, fileSet := lookup("KAKATTA_SECRET_KEY_FILE")
	return runtimeKeyConfig{inline: inline, inlineSet: inlineSet, file: file, fileSet: fileSet}
}

func (c runtimeKeyConfig) load(paths config.Paths) (key [backup.MasterKeyBytes]byte, source backup.KeySource, keyFile string, err error) {
	loaded, err := config.LoadMasterKey(paths, func(name string) (string, bool) {
		switch name {
		case "KAKATTA_SECRET_KEY":
			return c.inline, c.inlineSet
		case "KAKATTA_SECRET_KEY_FILE":
			return c.file, c.fileSet
		default:
			return "", false
		}
	})
	if err != nil {
		return key, "", "", err
	}
	defer clear(loaded)
	copy(key[:], loaded)
	source = backup.KeyDefaultFile
	if c.inlineSet {
		source = backup.KeyEnvironment
	} else if c.fileSet {
		source = backup.KeyExternalFile
		keyFile, err = absoluteKeyPath(c.file)
		if err != nil {
			clear(key[:])
			return key, "", "", err
		}
	}
	return key, source, keyFile, nil
}
