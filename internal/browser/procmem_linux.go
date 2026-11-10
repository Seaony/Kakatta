package browser

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// listProcs 读 /proc/<pid>/stat：第 4 个字段是父进程号，第 24 个是常驻内存页数。精简的容器镜像里可能没有 ps，所以直接读 /proc。
func listProcs() ([]proc, error) {
	dirs, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}
	page := int64(os.Getpagesize())
	var out []proc
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "stat"))
		if err != nil {
			continue
		}
		s := string(b)
		// 进程名在括号里，可能带空格，从最后一个右括号之后开始数字段。
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 22 {
			continue
		}
		pid, _ := strconv.Atoi(filepath.Base(d))
		ppid, _ := strconv.Atoi(f[1])
		pages, _ := strconv.ParseInt(f[21], 10, 64)
		out = append(out, proc{pid: pid, ppid: ppid, rss: pages * page})
	}
	return out, nil
}
