package remote

import (
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func stopProcess(pid int) {
	if pid < 1 {
		return
	}
	syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	syscall.Kill(-pid, syscall.SIGKILL)
}
