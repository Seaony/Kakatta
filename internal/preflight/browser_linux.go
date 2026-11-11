package preflight

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/remote"
)

func browserChecks(parent context.Context, o Options) (checks []Check) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	profile, err := os.MkdirTemp(o.DataDir, ".preflight-")
	if err != nil {
		return append(browserSetupFailure("无法创建独立自检目录", "检查数据挂载及 PUID/PGID"), Check{ID: "cleanup", Passed: true, Detail: "没有启动浏览器或画面进程"})
	}
	var display *browser.Display
	var chrome *browser.Session
	var view *remote.Session
	var logs *diagnostics.Recorder
	var writers []io.WriteCloser
	defer func() {
		var closeErr error
		if view != nil {
			closeErr = errors.Join(closeErr, view.Close())
		}
		if chrome != nil {
			pid := chrome.PID()
			chrome.Close()
			if pid > 0 && syscall.Kill(pid, 0) != syscall.ESRCH {
				closeErr = errors.Join(closeErr, errors.New("自检浏览器进程仍存在"))
			}
		}
		if display != nil {
			closeErr = errors.Join(closeErr, display.Close())
		}
		for _, writer := range writers {
			closeErr = errors.Join(closeErr, writer.Close())
		}
		if logs != nil {
			closeErr = errors.Join(closeErr, logs.Err(), logs.Close())
		}
		if closeErr == nil {
			closeErr = os.RemoveAll(profile)
		}
		cleanup := Check{ID: "cleanup", Passed: closeErr == nil, Detail: "本次浏览器、虚拟屏幕和远程画面均已关闭，自检目录已删除"}
		if closeErr != nil {
			cleanup.Detail = "自检资源清理失败：" + closeErr.Error()
			cleanup.Action = "查看自检日志，停止遗留进程；必要时重启容器后重新自检"
		}
		checks = append(checks, cleanup)
	}()
	logs, err = diagnostics.Open(o.LogDir, o.Timezone)
	if err != nil {
		return browserSetupFailure("无法打开浏览器自检日志", "检查日志目录权限和剩余磁盘空间")
	}
	outputs := map[string]io.Writer{}
	for _, name := range []string{"chrome", "xvfb", "x11vnc"} {
		writer, err := logs.ProcessLog(name)
		if err != nil {
			return browserSetupFailure("无法写入浏览器自检日志", "检查日志目录权限和剩余磁盘空间")
		}
		writers = append(writers, writer)
		outputs[name] = writer
	}
	id, err := temporaryAccountID()
	if err != nil {
		return browserSetupFailure(err.Error(), "停止旧的自检后重试；不要结束正在使用的真实账号进程")
	}
	display, err = browser.StartDisplayWithLog(ctx, id, outputs["xvfb"])
	if err != nil {
		checks = append(checks, Check{ID: "display", Detail: "虚拟屏幕启动失败：" + err.Error(), Action: "查看 xvfb 日志，确认镜像包含 Xvfb 并允许启动虚拟屏幕"})
		checks = append(checks, Check{ID: "chrome", Detail: "未执行：虚拟屏幕未就绪", Action: "先修复虚拟屏幕启动问题"}, Check{ID: "remote", Detail: "未执行：虚拟屏幕未就绪", Action: "先修复虚拟屏幕启动问题"})
		return checks
	}
	checks = append(checks, Check{ID: "display", Passed: true, Detail: "独立虚拟屏幕已启动", Values: map[string]any{"display": display.Name()}})
	chrome, err = browser.Start(ctx, browser.Options{
		ProfileDir: filepath.Join(profile, "browser"), AccountID: id,
		Env: []string{"DISPLAY=" + display.Name()}, TimeZone: o.Timezone,
		WebGL: o.WebGL, Output: outputs["chrome"],
	})
	chromeCheck := Check{ID: "chrome", Action: "查看 chrome 日志，核对 Chrome、沙盒和容器 seccomp 设置"}
	if err != nil {
		chromeCheck.Detail = "浏览器启动失败：" + err.Error()
	} else {
		currentURL, urlErr := chrome.URLContext(ctx)
		version, versionErr := chrome.Version(ctx)
		switch {
		case urlErr != nil:
			chromeCheck.Detail = "浏览器已启动，但无法读取空白页：" + urlErr.Error()
		case currentURL != "about:blank":
			chromeCheck.Detail = "浏览器初始页面不是本地空白页"
		case versionErr != nil:
			chromeCheck.Detail = "浏览器已启动，但无法读取实际版本：" + versionErr.Error()
		default:
			chromeCheck.Passed, chromeCheck.Detail, chromeCheck.Action = true, "浏览器已打开本地空白页，未打开外部网页", ""
			chromeCheck.Values = map[string]any{"version": version, "url": "about:blank", "sandbox": true, "webgl": o.WebGL}
		}
	}
	checks = append(checks, chromeCheck)
	view, err = remote.StartWithLog(ctx, display.Name(), outputs["x11vnc"])
	remoteCheck := Check{ID: "remote", Action: "查看 x11vnc 日志，确认镜像完整；必要时重启容器"}
	if err != nil {
		remoteCheck.Detail = "远程画面启动失败：" + err.Error()
	} else {
		remoteCheck.Passed, remoteCheck.Detail, remoteCheck.Action = true, "x11vnc 已启动并完成本机 RFB 握手", ""
	}
	checks = append(checks, remoteCheck)
	if err := logs.Err(); err != nil {
		checks = append(checks, Check{ID: "browser_logs", Detail: "浏览器自检日志写入失败：" + err.Error(), Action: "检查剩余磁盘空间和日志目录权限"})
	}
	return checks
}

func browserSetupFailure(detail, action string) []Check {
	var checks []Check
	for _, id := range []string{"display", "chrome", "remote"} {
		checks = append(checks, Check{ID: id, Detail: detail, Action: action})
	}
	return checks
}

func temporaryAccountID() (int, error) {
	// 只挑空闲的高位端口和屏幕，不接触账号原有的配置目录和锁。
	for id := 12767; id > 12667; id-- {
		number := strconv.Itoa(100 + id)
		occupied := false
		for _, name := range []string{"/tmp/.X11-unix/X" + number, "/tmp/.X" + number + "-lock"} {
			if _, err := os.Lstat(name); !os.IsNotExist(err) {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		port, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(20000+id))
		if err != nil {
			continue
		}
		if err := port.Close(); err != nil {
			return 0, err
		}
		return id, nil
	}
	return 0, fmt.Errorf("没有空闲的自检端口和虚拟屏幕")
}
