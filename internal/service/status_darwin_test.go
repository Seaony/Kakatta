//go:build darwin

package service

import "testing"

func TestDarwinMemoryKeepsUnavailableUsageUnknown(t *testing.T) {
	total, used := platformMemory()
	if total == nil || *total <= 0 {
		t.Fatal("没有读取系统的总内存")
	}
	if used != nil {
		t.Fatal("不能用进程堆内存冒充系统已用内存")
	}
}
