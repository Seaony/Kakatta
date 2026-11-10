package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/store"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.LookupEnv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, lookup config.Lookup) error {
	flags := flag.NewFlagSet("seed", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() { fmt.Fprintln(output, "用法：seed --data <独立空目录>") }
	data := flags.String("data", "", "独立的空样例目录，必须明确指定")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *data == "" || flags.NArg() != 0 {
		return errors.New("用法：seed --data <独立空目录>")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := config.Load([]string{"--data", *data}, lookup)
	if err != nil {
		return err
	}
	if err := emptyDirectory(cfg.DataDir, false); err != nil {
		return err
	}
	lock, err := config.AcquireServerLock(cfg.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	// 拿锁前可能有另一个进程刚完成写入，不能只检查一次。
	if err := emptyDirectory(cfg.DataDir, true); err != nil {
		return err
	}
	paths, err := config.CreateDataDirectories(cfg.DataDir)
	if err != nil {
		return err
	}
	key, err := config.LoadMasterKey(paths, lookup)
	if err != nil {
		return err
	}
	clear(key)
	database, err := store.Open(ctx, paths.Database)
	if err != nil {
		return err
	}
	defer database.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	preferences, err := appsettings.New(ctx, database.DB(), cfg.Environment, func() time.Time { return now })
	if err != nil {
		return err
	}
	snapshot, err := preferences.Get(ctx)
	if err != nil {
		return err
	}
	var timezone string
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &timezone); err != nil {
		return err
	}
	counts, err := populate(ctx, database.DB(), now, timezone)
	if err != nil {
		return fmt.Errorf("样例写入失败，事务已回滚：%w", err)
	}
	_, err = fmt.Fprintf(output, "已写入 %d 个停用任务、%d 件虚构商品、%d 条结果。首次启动仍需初始化管理员。\n", counts.tasks, counts.listings, counts.findings)
	if err != nil {
		return fmt.Errorf("样例已写入，但无法输出结果：%w", err)
	}
	return nil
}

func emptyDirectory(path string, locked bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !locked {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("样例路径必须是真实的空目录")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if locked && entry.Name() == ".server.lock" {
			continue
		}
		return errors.New("样例目录已有内容，请另外指定独立空目录；不会覆盖或清除已有数据")
	}
	return nil
}
