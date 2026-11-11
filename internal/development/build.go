//go:build kakatta_dev

package development

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type candidate struct {
	Generation string `json:"generation"`
	Binary     string `json:"binary"`
	Error      string `json:"error"`
}

type buildProcess struct {
	HelperPID int `json:"helper_pid"`
	GoPID     int `json:"go_pid"`
}

func lockBuild(state string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(state, "build.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func writePrivate(path string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".dev-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Build 先产出独立文件，只有完整编译结果才会进入候选清单。
func Build(ctx context.Context, root, state string, output io.Writer) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	lock, err := lockBuild(state)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Stat(filepath.Join(state, "stop-build")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(state, "candidates"), 0700); err != nil {
		return err
	}
	next := candidate{Generation: strconv.FormatInt(time.Now().UnixNano(), 36)}
	next.Binary = filepath.Join(state, "candidates", "backend-"+next.Generation)
	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-tags", "kakatta_dev", "-o", next.Binary, "./cmd/kakatta")
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, output, output
	buildErr := cmd.Start()
	if buildErr == nil {
		raw, _ := json.Marshal(buildProcess{HelperPID: os.Getpid(), GoPID: cmd.Process.Pid})
		if err := writePrivate(filepath.Join(state, "build-process.json"), raw); err != nil {
			_ = cmd.Wait()
			return err
		}
		buildErr = cmd.Wait()
	}
	if buildErr != nil {
		_ = os.Remove(next.Binary)
		next.Binary, next.Error = "", "后端编译失败，继续使用原后端；请检查编译日志"
	}
	raw, err := json.Marshal(next)
	if err == nil {
		err = writePrivate(filepath.Join(state, "candidate.json"), raw)
	}
	if err != nil {
		return err
	}
	return buildErr
}

func (h *Host) finishBuild() error {
	if err := writePrivate(filepath.Join(h.config.State, "stop-build"), []byte("stopping\n")); err != nil {
		return err
	}
	h.log("manager", "后端已退出，等待当前 Go 编译完成后关闭编译监听")
	lock, err := lockBuild(h.config.State)
	if err != nil {
		return err
	}
	return lock.Close()
}

func airConfig(root, state, executable string, poll bool) string {
	quote := strconv.Quote
	command := shellQuote(executable) + " build --root " + shellQuote(root) + " --state " + shellQuote(state)
	return fmt.Sprintf(`root = %s
tmp_dir = %s
env_files = []
[build]
cmd = %s
entrypoint = ["/usr/bin/true"]
log = %s
include_ext = ["go", "json", "sql", "txt"]
include_dir = ["cmd", "internal"]
include_file = ["go.mod", "go.sum"]
exclude_dir = ["build", "data", ".git", "web", "node_modules"]
exclude_regex = ["_test\\.go"]
exclude_unchanged = true
follow_symlink = false
poll = %t
poll_interval = 500
delay = 300
stop_on_error = false
send_interrupt = true
kill_delay = "1s"
[log]
time = true
[misc]
clean_on_exit = false
[screen]
clear_on_rebuild = false
`, quote(root), quote(filepath.Join(state, "air")), quote(command), quote(filepath.Join(state, "build-errors.log")), poll)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
