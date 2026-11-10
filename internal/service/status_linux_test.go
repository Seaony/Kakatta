package service

import (
	"errors"
	"math"
	"os"
	"strconv"
	"testing"
)

func statusMemoryFiles(files map[string]string, failures map[string]error) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if err := failures[path]; err != nil {
			return nil, err
		}
		if value, ok := files[path]; ok {
			return []byte(value), nil
		}
		return nil, os.ErrNotExist
	}
}

func TestLinuxMemoryUsesContainerLimitAndMatchingUsage(t *testing.T) {
	for _, test := range []struct {
		name, group, limitPath, usagePath, limit string
	}{
		{"v2", "0::/containers/demo\n", "/sys/fs/cgroup/containers/demo/memory.max", "/sys/fs/cgroup/containers/demo/memory.current", "268435456\n"},
		{"v2挂载根", "0::/\n", "/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory.current", "268435456\n"},
		{"v1", "8:memory:/containers/demo\n", "/sys/fs/cgroup/memory/containers/demo/memory.limit_in_bytes", "/sys/fs/cgroup/memory/containers/demo/memory.usage_in_bytes", "268435456\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{
				"/proc/meminfo":     "MemTotal: 8388608 kB\nMemAvailable: 1048576 kB\n",
				"/proc/self/cgroup": test.group, test.limitPath: test.limit, test.usagePath: "67108864\n",
			}
			total, used := linuxMemory(statusMemoryFiles(files, nil))
			if total == nil || used == nil || *total != 268435456 || *used != 67108864 {
				t.Fatalf("没有使用同一容器的额度和用量：%v %v", total, used)
			}
		})
	}
}

func TestLinuxMemoryUsesTightestAncestorAndHostWhenUnlimited(t *testing.T) {
	files := map[string]string{
		"/proc/meminfo":                              "MemTotal: 1048576 kB\nMemAvailable: 262144 kB\n",
		"/proc/self/cgroup":                          "0::/parent/child\n",
		"/sys/fs/cgroup/parent/child/memory.max":     "536870912",
		"/sys/fs/cgroup/parent/child/memory.current": "100",
		"/sys/fs/cgroup/parent/memory.max":           "268435456",
		"/sys/fs/cgroup/parent/memory.current":       "200",
		"/sys/fs/cgroup/memory.max":                  "max",
	}
	total, used := linuxMemory(statusMemoryFiles(files, nil))
	if total == nil || used == nil || *total != 268435456 || *used != 200 {
		t.Fatalf("忽略了更小的父层额度：%v %v", total, used)
	}
	files["/sys/fs/cgroup/parent/child/memory.max"] = "max"
	files["/sys/fs/cgroup/parent/memory.max"] = "max"
	total, used = linuxMemory(statusMemoryFiles(files, nil))
	if total == nil || used == nil || *total != 1<<30 || *used != 768<<20 {
		t.Fatalf("无限额时没有回到主机读数：%v %v", total, used)
	}
	files["/proc/self/cgroup"] = "8:memory:/\n"
	files["/sys/fs/cgroup/memory/memory.limit_in_bytes"] = "9223372036854771712"
	files["/sys/fs/cgroup/memory/memory.usage_in_bytes"] = "100"
	total, used = linuxMemory(statusMemoryFiles(files, nil))
	if total == nil || used == nil || *total != 1<<30 || *used != 768<<20 {
		t.Fatal("v1无限额哨兵被当成真实容器容量")
	}
}

func TestLinuxMemoryKeepsUnknownInsteadOfInventingZero(t *testing.T) {
	for _, test := range []struct {
		name, meminfo, limit, usage string
		limitError                  error
		wantTotal, wantUsed         *int64
	}{
		{name: "读不到主机"},
		{name: "主机缺可用量", meminfo: "MemTotal: 1024 kB\n", wantTotal: statusNumber(1 << 20)},
		{name: "可用量超过总量", meminfo: "MemTotal: 1024 kB\nMemAvailable: 2048 kB\n", wantTotal: statusNumber(1 << 20)},
		{name: "主机总量负数", meminfo: "MemTotal: -1 kB\nMemAvailable: 1 kB\n"},
		{name: "主机总量溢出", meminfo: "MemTotal: " + strconv.FormatInt(math.MaxInt64, 10) + " kB\nMemAvailable: 1 kB\n"},
		{name: "容器用量坏了", meminfo: "MemTotal: 1024 kB\nMemAvailable: 512 kB\n", limit: "1000", usage: "bad", wantTotal: statusNumber(1000)},
		{name: "容器用量未知", limit: "1000", wantTotal: statusNumber(1000)},
		{name: "容器限额坏了", meminfo: "MemTotal: 1024 kB\nMemAvailable: 512 kB\n", limit: "bad", usage: "0"},
		{name: "容器限额读取失败", meminfo: "MemTotal: 1024 kB\nMemAvailable: 512 kB\n", limitError: errors.New("读取失败")},
		{name: "已知零用量", limit: "1000", usage: "0", wantTotal: statusNumber(1000), wantUsed: statusNumber(0)},
		{name: "暂时超过限额", limit: "1000", usage: "1100", wantTotal: statusNumber(1000), wantUsed: statusNumber(1100)},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{"/proc/self/cgroup": "0::/\n"}
			if test.meminfo != "" {
				files["/proc/meminfo"] = test.meminfo
			}
			if test.limit != "" {
				files["/sys/fs/cgroup/memory.max"] = test.limit
			}
			if test.usage != "" {
				files["/sys/fs/cgroup/memory.current"] = test.usage
			}
			total, used := linuxMemory(statusMemoryFiles(files, map[string]error{"/sys/fs/cgroup/memory.max": test.limitError}))
			if !sameStatusNumber(total, test.wantTotal) || !sameStatusNumber(used, test.wantUsed) {
				t.Fatalf("未知值被伪造：total=%v used=%v", total, used)
			}
		})
	}
}

func statusNumber(value int64) *int64 { return &value }
func sameStatusNumber(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
