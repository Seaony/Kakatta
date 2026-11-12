package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func main() {
	if err := runCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "实验工具：", diagnostics.RedactText(err.Error()))
		os.Exit(1)
	}
}
func parseOptions(args []string, errOut io.Writer) (options, string, error) {
	o := options{}
	flags := flag.NewFlagSet("experiment", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.StringVar(&o.Data, "data", "/data", "数据目录")
	flags.StringVar(&o.Listen, "listen", "127.0.0.1:8080", "实验网页监听地址")
	flags.StringVar(&o.Keyword, "keyword", "相机", "只搜索这一个关键词")
	flags.IntVar(&o.Account, "account", 1, "实验账号编号")
	flags.IntVar(&o.Details, "details", 3, "单次打开两到三个详情")
	flags.BoolVar(&o.Fixture, "fixture", false, "仅使用本机假页面，不访问闲鱼")
	flags.BoolVar(&o.Once, "once", false, "仅限假页面：跑一次并退出")
	flags.BoolVar(&o.Check, "check", false, "仅做本地环境自检，不访问闲鱼")
	flags.StringVar(&o.WebGL, "webgl", "off", "实验渲染方式：off或llvmpipe")
	flags.BoolVar(&o.Filters.PersonalOnly, "personal-only", true, "只看个人闲置")
	flags.BoolVar(&o.Filters.FreeShipping, "free-shipping", false, "只看包邮")
	var settingsPath, proxyFile, region, minPrice, maxPrice string
	flags.StringVar(&settingsPath, "config", "", "实验设置JSON，默认数据目录的experiment-settings.json")
	flags.StringVar(&proxyFile, "proxy-file", "", "只从该文件读取固定代理地址，避免密码出现在命令行")
	flags.StringVar(&region, "region", "", "地区，写成省/市/区")
	flags.StringVar(&minPrice, "min-price", "", "最低价，单位元")
	flags.StringVar(&maxPrice, "max-price", "", "最高价，单位元")
	if err := flags.Parse(args); err != nil {
		return o, "", err
	}
	if flags.NArg() != 0 {
		return o, "", errors.New("没有位置参数，请用 --help 查看选项")
	}
	o.Keyword = strings.TrimSpace(o.Keyword)
	if o.Keyword == "" || len([]rune(o.Keyword)) > 50 {
		return o, "", errors.New("关键词需要1到50个字")
	}
	if o.Details < 2 || o.Details > 3 {
		return o, "", errors.New("实验每次只打开2到3个详情")
	}
	if o.Account < 1 || o.Account > 12767 {
		return o, "", errors.New("账号编号超出固定端口范围")
	}
	if o.Once && !o.Fixture {
		return o, "", errors.New("--once只能配合--fixture，真实实验必须由人在远程画面里看着")
	}
	if o.WebGL != "off" && o.WebGL != "llvmpipe" {
		return o, "", errors.New("渲染方式只能是off或llvmpipe")
	}
	parts := strings.Split(region, "/")
	if region != "" {
		if len(parts) < 2 || len(parts) > 3 {
			return o, "", errors.New("地区应写成省/市或省/市/区")
		}
		o.Filters.Province = strings.TrimSpace(parts[0])
		o.Filters.City = strings.TrimSpace(parts[1])
		if len(parts) == 3 {
			o.Filters.District = strings.TrimSpace(parts[2])
		}
		if o.Filters.Province == "" || o.Filters.City == "" || len(parts) == 3 && o.Filters.District == "" {
			return o, "", errors.New("地区的省、市、区不能留空")
		}
	}
	for _, input := range []struct {
		text string
		out  **int64
	}{{minPrice, &o.Filters.MinPriceCents}, {maxPrice, &o.Filters.MaxPriceCents}} {
		if input.text != "" {
			n, ok := xianyu.ParsePriceCents(input.text)
			if !ok {
				return o, "", errors.New("价格应是非负数字")
			}
			*input.out = &n
		}
	}
	if o.Filters.MinPriceCents != nil && o.Filters.MaxPriceCents != nil && *o.Filters.MinPriceCents > *o.Filters.MaxPriceCents {
		return o, "", errors.New("最低价不能高于最高价")
	}
	if proxyFile != "" {
		b, err := os.ReadFile(proxyFile)
		if err != nil {
			return o, "", fmt.Errorf("读取代理文件失败：%w", err)
		}
		u, err := url.Parse(strings.TrimSpace(string(b)))
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "socks5") {
			return o, "", errors.New("代理地址应使用http或socks5")
		}
		o.Proxy = u
	}
	if settingsPath == "" {
		settingsPath = filepath.Join(o.Data, "experiment-settings.json")
	}
	return o, settingsPath, nil
}
func runCommand(args []string, out, errOut io.Writer) (result error) {
	o, settingsPath, err := parseOptions(args, errOut)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = os.MkdirAll(o.Data, 0700); err != nil {
		return err
	}
	s, err := loadSettings(settingsPath)
	if err != nil {
		timezone := defaultSettings().Timezone
		if _, failure := time.LoadLocation(timezone); failure != nil {
			timezone = "UTC"
		}
		return recordStartupFailure(o, timezone, err)
	}
	if o.Check {
		return runSelfCheck(o, s, out)
	}
	c, err := newController(o, s)
	if err != nil {
		return recordStartupFailure(o, s.Timezone, err)
	}
	defer c.Close()
	c.sink, c.stderr = out, errOut
	defer func() {
		if result != nil {
			c.log("工具停止", map[string]any{"分类": experimentErrorClass(result), "原因": diagnosticReason(result)})
		}
	}()
	if o.Once {
		return c.run(context.Background())
	}
	listener, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return fmt.Errorf("无法监听 %s，端口可能已被占用；请换成 --listen 127.0.0.1:8180：%w", o.Listen, err)
	}
	defer listener.Close()
	handler := experimentHandler(c)
	server := &http.Server{Addr: o.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	c.log("实验页面已启动，请先扫码登录，结束并等账号间隔后再单次运行", map[string]any{"监听地址": listener.Addr().String(), "日志目录": experimentLogDir(o)})
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
	return nil
}

func experimentHandler(c *controller) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := c.recorder.Err(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "diagnostics unavailable")
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", remote.ControlPagePolicy())
		w.Write(remote.ControlPage())
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		c.checkManual(r.Context())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c.snapshot())
	})
	start := func(mode string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if mode == "verify" {
				if err := c.verify(r.Context()); err != nil {
					c.log("操作停止", map[string]any{"步骤": "打开验证画面", "分类": experimentErrorClass(err), "原因": diagnosticReason(err)})
					http.Error(w, diagnosticReason(err), 409)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				return
			}
			job, err := c.reserve(context.Background(), mode)
			if err != nil {
				c.log("操作停止", map[string]any{"步骤": mode, "分类": experimentErrorClass(err), "原因": diagnosticReason(err)})
				http.Error(w, diagnosticReason(err), http.StatusConflict)
				return
			}
			go func() {
				var err error
				if mode == "login" {
					err = c.loginReserved(job)
				} else {
					err = c.runReserved(job)
				}
				if err != nil {
					c.log("操作停止", map[string]any{"原因": err.Error()})
				}
			}()
			w.WriteHeader(http.StatusAccepted)
		}
	}
	mux.HandleFunc("POST /session", start("login"))
	mux.HandleFunc("POST /run", start("run"))
	mux.HandleFunc("POST /verify", start("verify"))
	mux.HandleFunc("POST /confirm", func(w http.ResponseWriter, r *http.Request) {
		if err := c.confirm(r.Context()); err != nil {
			c.log("操作停止", map[string]any{"步骤": "确认验证完成", "分类": experimentErrorClass(err), "原因": diagnosticReason(err)})
			http.Error(w, diagnosticReason(err), 409)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /close", func(w http.ResponseWriter, r *http.Request) { c.closeSession(); w.WriteHeader(204) })
	mux.HandleFunc("/remote/", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		res := c.res
		c.mu.Unlock()
		if res == nil || res.remote == nil {
			http.Error(w, "还没有打开浏览器", 409)
			return
		}
		view := res.remote.HandlerWithViewer(func(*http.Request) bool { return true }, func(context.Context) error {
			c.viewing(res)
			return nil
		})
		http.StripPrefix("/remote", view).ServeHTTP(w, r)
	})
	return new(http.CrossOriginProtection).Handler(noCache(mux))
}
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
