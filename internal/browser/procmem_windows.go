package browser

import (
	"os/exec"
	"strconv"
	"strings"
)

// listProcs 用 PowerShell 读 Win32_Process，WorkingSetSize 的单位是字节。
func listProcs() ([]proc, error) {
	cmd := `Get-CimInstance Win32_Process | ForEach-Object { "$($_.ProcessId) $($_.ParentProcessId) $($_.WorkingSetSize)" }`
	b, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", cmd).Output()
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
		rss, _ := strconv.ParseInt(f[2], 10, 64)
		out = append(out, proc{pid: pid, ppid: ppid, rss: rss})
	}
	return out, nil
}
