//go:build !linux

package remote

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}
func stopProcess(pid int)            {}
