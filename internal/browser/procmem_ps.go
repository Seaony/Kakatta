//go:build !linux && !windows

package browser

import (
	"os/exec"
	"strconv"
	"strings"
)

// listProcs 用 ps 列出所有进程，rss 的单位是 KB。
func listProcs() ([]proc, error) {
	b, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,rss=").Output()
	if err != nil {
		return nil, err
	}
	var out []proc
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		kb, _ := strconv.ParseInt(f[2], 10, 64)
		out = append(out, proc{pid: pid, ppid: ppid, rss: kb * 1024})
	}
	return out, nil
}
