//go:build kakatta_dev

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Seaony/Kakatta/internal/development"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("devhost", flag.ContinueOnError)
	build := len(args) > 0 && args[0] == "build"
	if build {
		args = args[1:]
	}
	root := flags.String("root", ".", "仓库目录")
	state := flags.String("state", "build/_dev", "开发管理文件目录")
	mode := flags.String("mode", "fake", "fake 或 real")
	data := flags.String("data", "build/_dev/data", "开发数据目录")
	listen := flags.String("listen", "127.0.0.1:18081", "开发控制地址")
	backend := flags.String("backend-listen", "127.0.0.1:18080", "后端回环地址")
	proxy := flags.String("proxy-listen", "", "容器内后端转发地址")
	air := flags.String("air", "build/_tools/air", "编译监听程序")
	fixture := flags.String("fixture", "", "外部本地夹具文件")
	poll := flags.Bool("poll", false, "改用轮询观察文件")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("开发管理命令不接受位置参数")
	}
	if build {
		return development.Build(context.Background(), *root, *state, os.Stdout)
	}
	host, err := development.New(development.Config{Mode: *mode, Root: *root, State: *state, Data: *data, Listen: *listen, BackendListen: *backend, ProxyListen: *proxy, Air: *air, Fixture: *fixture, Poll: *poll, Output: os.Stdout})
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		for range signals {
			host.RequestStop()
		}
	}()
	return host.Run(context.Background())
}
