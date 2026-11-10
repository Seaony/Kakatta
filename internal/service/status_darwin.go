//go:build darwin

package service

import (
	"math"

	"golang.org/x/sys/unix"
)

func platformMemory() (*int64, *int64) {
	value, err := unix.SysctlUint64("hw.memsize")
	if err != nil || value == 0 || value > math.MaxInt64 {
		return nil, nil
	}
	total := int64(value)
	// 没有可靠的系统用量读取时保留未知，不能用进程的堆占用代替。
	return &total, nil
}
