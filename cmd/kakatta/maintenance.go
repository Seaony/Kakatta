package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/store"
	"golang.org/x/term"
)

func maintenance(ctx context.Context, command string, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(output)
	data := flags.String("data", "./data", "数据目录")
	disable := false
	if command == "reset-password" {
		flags.BoolVar(&disable, "disable-totp", false, "同时关闭两步验证")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *data == "" {
		return errors.New("维护命令参数无效")
	}
	restoreLock, err := config.AcquireRestoreLock(*data, false)
	if err != nil {
		return err
	}
	defer restoreLock.Close()
	if _, err := os.Lstat(filepath.Join(*data, config.RestoreStateName)); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return errors.New("无法检查恢复状态，维护命令未执行")
		}
		return errors.New("恢复尚未完成，维护命令未执行")
	}
	path := filepath.Join(*data, "kakatta.db")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("数据目录里没有已有数据库")
	}
	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	service, _, err := auth.New(ctx, auth.Dependencies{DB: db.DB()})
	if err != nil {
		return err
	}
	status, err := service.Setup(ctx)
	if err != nil {
		return err
	}
	if !status.Initialized {
		return errors.New("尚未初始化管理员")
	}
	if command == "disable-totp" {
		if err := service.DisableTOTPForCLI(ctx); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "已关闭两步验证")
		return err
	}
	password, err := readPassword(input, output)
	if err != nil {
		return err
	}
	defer clear(password)
	if err := service.ResetPassword(ctx, string(password), disable); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "已重置密码，全部会话和令牌已失效")
	return err
}

func readPassword(input io.Reader, output io.Writer) ([]byte, error) {
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(output, "新密码：")
		password, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(output)
		return password, err
	}
	reader := bufio.NewReader(io.LimitReader(input, (1<<20)+1))
	password, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("无法读取新密码")
	}
	if len(password) > 1<<20 {
		return nil, errors.New("新密码输入过长")
	}
	return []byte(strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")), nil
}
