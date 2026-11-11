//go:build kakatta_dev

package development

import (
	"io"
	"os"
	"os/exec"
	"syscall"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startChild(name string, args []string, dir string, output io.Writer) (*child, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, output, output
	// 终端的 Ctrl+C 只交给管理器，后端必须先完成排空。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &child{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		if stream, ok := output.(*logWriter); ok {
			stream.flush()
		}
		close(p.done)
	}()
	return p, nil
}

func (p *child) interrupt() error {
	select {
	case <-p.done:
		return nil
	default:
		return p.cmd.Process.Signal(os.Interrupt)
	}
}
