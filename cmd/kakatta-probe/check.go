package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func cmdCheck(ctx context.Context, args []string) int {
	var c common
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if err := c.prepare(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError
	}
	defer c.j.Close()
	log := c.j.with(map[string]any{"命令": "check"})
	c.state.log = log

	s, r, err := c.open(ctx, log)
	if err != nil {
		return c.exitFor(err, log)
	}
	err = r.CheckLogin(ctx)
	s.Close()
	if err == nil {
		log("登录还在", nil)
	}
	return c.exitFor(err, log)
}
