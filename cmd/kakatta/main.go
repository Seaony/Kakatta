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

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/web"
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

func serve(ctx context.Context, cfg config.Config, lookup config.Lookup, logger *slog.Logger) error {
	paths, err := config.CreateDataDirectories(cfg.DataDir)
	if err != nil {
		return err
	}
	key, err := config.LoadMasterKey(paths, lookup)
	if err != nil {
		return err
	}
	cipher, err := security.NewCipher(key)
	clear(key)
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, paths.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	preferences, err := appsettings.New(ctx, db.DB(), cfg.Environment, nil)
	if err != nil {
		return err
	}
	authentication, bootstrap, err := auth.New(ctx, auth.Dependencies{DB: db.DB(), Cipher: cipher})
	if err != nil {
		return err
	}
	events := eventstore.Store{DB: db.DB()}
	handler, err := server.New(server.Dependencies{
		Auth: authentication, Settings: preferences, Health: db.DB().PingContext, Web: web.Assets(),
		LoginFailure: func(ctx context.Context, event server.LoginFailureEvent) error {
			if event.Active {
				_, err := events.Raise(ctx, "login_failures", 0, locales.Message{Code: "security.login_failures", Args: map[string]any{"count": event.Limit, "ip": event.IP}})
				return err
			}
			_, err := events.Resolve(ctx, "login_failures", 0)
			return err
		},
	})
	if err != nil {
		return err
	}
	defer handler.Close()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute,
		MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return ctx },
	}
	if bootstrap.Token != "" {
		logger.Info("首次初始化令牌，只显示这一次", "token", bootstrap.Token, "expires_at", bootstrap.ExpiresAt)
	}
	logger.Info("服务已启动", "listen", listener.Addr().String())
	finished := make(chan error, 1)
	go func() { finished <- httpServer.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		_ = handler.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
			<-finished
			return err
		}
		<-finished
		return nil
	}
}
