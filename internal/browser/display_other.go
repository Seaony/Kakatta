//go:build !linux

package browser

import (
	"context"
	"fmt"
	"io"
)

type Display struct{}

func StartDisplay(context.Context, int) (*Display, error) {
	return nil, fmt.Errorf("虚拟屏幕需要 Linux 容器")
}

func StartDisplayWithLog(context.Context, int, io.Writer) (*Display, error) {
	return nil, fmt.Errorf("虚拟屏幕需要 Linux 容器")
}

func (*Display) Name() string { return "" }
func (*Display) PID() int     { return 0 }
func (*Display) Close() error { return nil }
