//go:build kakatta_dev

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/devfixture"
	"github.com/Seaony/Kakatta/internal/resources"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type developmentOptions struct {
	mode, fixture, controlFile string
	metadata                   devfixture.Metadata
}

func runDevelopment(ctx context.Context, args []string, output io.Writer, lookup config.Lookup) (bool, error) {
	if len(args) == 0 || args[0] != "dev" {
		return false, nil
	}
	flags := flag.NewFlagSet("dev", flag.ContinueOnError)
	flags.SetOutput(output)
	var options developmentOptions
	flags.StringVar(&options.mode, "mode", "fake", "假数据或真实容器")
	flags.StringVar(&options.fixture, "fixture", "", "本地假服务描述")
	flags.StringVar(&options.controlFile, "control-token-file", "", "管理进程的私有凭据文件")
	data := flags.String("data", "build/_dev/data", "开发数据目录")
	listen := flags.String("listen", "127.0.0.1:18080", "回环监听地址")
	if err := flags.Parse(args[1:]); err != nil {
		return true, err
	}
	if flags.NArg() != 0 || options.mode != "fake" && options.mode != "real" || !developmentLoopback(*listen) {
		return true, errors.New("开发入口只接受 fake/real 模式和数字回环监听地址")
	}
	if options.mode == "real" && runtime.GOOS != "linux" {
		return true, errors.New("真实站点开发请使用 bun run dev:real 的 Linux 容器")
	}
	if options.mode == "fake" {
		absolute, err := filepath.Abs(*data)
		if err != nil {
			return true, err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil || !strings.Contains(filepath.ToSlash(resolved), "/build/_dev/") {
			return true, errors.New("假数据开发必须使用 build/_dev 下的独立目录")
		}
		original := lookup
		if original == nil {
			original = os.LookupEnv
		}
		lookup = func(key string) (string, bool) {
			if key == "KAKATTA_SECRET_KEY" || key == "KAKATTA_SECRET_KEY_FILE" || key == "KAKATTA_TRUSTED_PROXIES" {
				return "", false
			}
			return original(key)
		}
	}
	cfg, err := config.Load([]string{"--data", *data, "--listen", *listen}, lookup)
	if err != nil {
		return true, err
	}
	if options.fixture != "" {
		file, err := os.Open(options.fixture)
		if err != nil {
			return true, errors.New("无法读取本地假服务描述")
		}
		decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&options.metadata)
		if err == nil && decoder.Decode(new(any)) != io.EOF {
			err = errors.New("多余的假服务描述")
		}
		file.Close()
		if err != nil {
			return true, errors.New("本地假服务描述格式无效")
		}
		for _, raw := range []string{options.metadata.Site.Home, options.metadata.ModelBaseURL, options.metadata.PushURL, options.metadata.ControlURL} {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || !developmentLoopback(u.Host) {
				return true, errors.New("假服务只允许数字回环地址")
			}
		}
	} else if options.mode == "fake" {
		return true, errors.New("假数据开发缺少本地假服务描述")
	}
	control, err := readDevelopmentControl(options.controlFile)
	if err != nil {
		return true, err
	}
	logger := slog.New(slog.NewJSONHandler(output, nil))
	factory := func(life context.Context, cfg config.Config, paths config.Paths, key [backup.MasterKeyBytes]byte, source backup.KeySource, generation string) (*runtimeGraph, error) {
		defer clear(key[:])
		site := web.Goofish()
		var resource service.ResourceFactory
		var resourceErr error
		if options.fixture != "" {
			site = web.Site{Home: options.metadata.Site.Home, APIHost: options.metadata.Site.APIHost}
			resource, resourceErr = resources.NewDevelopmentNative(site)
		} else {
			resource, resourceErr = resources.New(site)
		}
		if resourceErr != nil {
			return nil, resourceErr
		}
		g, err := newRuntimeGraphWithResources(life, cfg, paths, key, source, generation, resource)
		if err != nil {
			return nil, err
		}
		g.core.SetDevelopmentSchedulerPaused(true)
		if options.mode == "fake" {
			if _, err := g.preferences.Apply(life, settings.Values{"general.update_check": json.RawMessage(`false`)}); err != nil {
				return nil, errors.Join(err, g.close(context.Background()))
			}
		}
		if options.mode == "fake" {
			status, err := g.authentication.Setup(life)
			if err == nil && !status.Initialized {
				_, err = g.authentication.Initialize(life, "开发者", rand.Text(), "本机假数据开发")
			}
			if err != nil {
				return nil, errors.Join(err, g.close(context.Background()))
			}
		}
		return g, nil
	}
	return true, serveRuntime(ctx, cfg, lookup, logger, factory, func(manager *runtimeManager) (http.Handler, error) {
		if options.mode == "fake" {
			if err := configureDevelopmentFixture(ctx, manager.graph, options.metadata); err != nil {
				return nil, err
			}
			manager.graph.core.SetDevelopmentSchedulerPaused(false)
		}
		return newDevelopmentHandler(manager, options.mode == "fake", control), nil
	})
}

func developmentLoopback(authority string) bool {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || port == "" {
		return false
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Zone() == "" && address.IsLoopback()
}

func readDevelopmentControl(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("开发管理凭据必须是私有普通文件")
	}
	data, err := os.ReadFile(path)
	token := strings.TrimSpace(string(data))
	if err != nil || len(token) < 26 || len(token) > 128 {
		return "", errors.New("开发管理凭据无效")
	}
	return token, nil
}
