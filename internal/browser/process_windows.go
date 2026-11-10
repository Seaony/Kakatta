package browser

import (
	"fmt"
	"os"
	"os/exec"
)

func configureProcessGroup(cmd *exec.Cmd) {}

func terminateProcessGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

func profileInUse(dir string) (bool, error) {
	return false, fmt.Errorf("浏览器运行环境需要 Linux 容器")
}
