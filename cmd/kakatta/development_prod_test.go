//go:build !kakatta_dev

package main

import (
	"context"
	"io"
	"testing"
)

func TestProductionHasNoDevelopmentCommand(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	if handled, _ := runDevelopment(t.Context(), []string{"dev"}, io.Discard, lookup); handled {
		t.Fatal("正式构建注册了开发命令")
	}
	if err := run(context.Background(), []string{"dev", "--mode", "fake"}, nil, io.Discard, lookup); err == nil {
		t.Fatal("正式入口接受了开发模式")
	}
}
