package preflight

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/diagnostics"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/remote"
)

func browserChecks(parent context.Context, o Options) (checks []Check) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	profile, err := os.MkdirTemp(o.DataDir, ".preflight-")
	if err != nil {
		return append(browserSetupFailure(text(o.Language, "profile_failed", nil), text(o.Language, "mount_remedy", nil)), Check{ID: "cleanup", Passed: true, Detail: text(o.Language, "processes_not_started", nil)})
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
				closeErr = errors.Join(closeErr, errors.New(text(o.Language, "chrome_remaining", nil)))
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
		cleanup := Check{ID: "cleanup", Passed: closeErr == nil, Detail: text(o.Language, "cleanup_done", nil)}
		if closeErr != nil {
			cleanup.Detail = text(o.Language, "cleanup_failed", map[string]any{"error": closeErr.Error()})
			cleanup.Action = text(o.Language, "cleanup_remedy", nil)
		}
		checks = append(checks, cleanup)
	}()
	logs, err = diagnostics.Open(o.LogDir, o.Timezone)
	if err != nil {
		return browserSetupFailure(text(o.Language, "logs_open_failed", nil), text(o.Language, "logs_remedy", nil))
	}
	outputs := map[string]io.Writer{}
	for _, name := range []string{"chrome", "xvfb", "x11vnc"} {
		writer, err := logs.ProcessLog(name)
		if err != nil {
			return browserSetupFailure(text(o.Language, "logs_write_failed", nil), text(o.Language, "logs_remedy", nil))
		}
		writers = append(writers, writer)
		outputs[name] = writer
	}
	id, err := temporaryAccountID(o.Language)
	if err != nil {
		return browserSetupFailure(err.Error(), text(o.Language, "port_remedy", nil))
	}
	display, err = browser.StartDisplayWithLog(ctx, id, outputs["xvfb"])
	if err != nil {
		checks = append(checks, Check{ID: "display", Detail: text(o.Language, "display_failed", map[string]any{"error": err.Error()}), Action: text(o.Language, "display_remedy", nil)})
		checks = append(checks, Check{ID: "chrome", Detail: text(o.Language, "display_not_ready", nil), Action: text(o.Language, "display_first_remedy", nil)}, Check{ID: "remote", Detail: text(o.Language, "display_not_ready", nil), Action: text(o.Language, "display_first_remedy", nil)})
		return checks
	}
	checks = append(checks, Check{ID: "display", Passed: true, Detail: text(o.Language, "display_started", nil), Values: map[string]any{"display": display.Name()}})
	chrome, err = browser.Start(ctx, browser.Options{
		ProfileDir: filepath.Join(profile, "browser"), AccountID: id,
		Env: []string{"DISPLAY=" + display.Name()}, TimeZone: o.Timezone,
		WebGL: o.WebGL, Output: outputs["chrome"],
	})
	chromeCheck := Check{ID: "chrome", Action: text(o.Language, "chrome_remedy", nil)}
	if err != nil {
		chromeCheck.Detail = text(o.Language, "chrome_start_failed", map[string]any{"error": err.Error()})
	} else {
		currentURL, urlErr := chrome.URLContext(ctx)
		version, versionErr := chrome.Version(ctx)
		switch {
		case urlErr != nil:
			chromeCheck.Detail = text(o.Language, "chrome_url_failed", map[string]any{"error": urlErr.Error()})
		case currentURL != "about:blank":
			chromeCheck.Detail = text(o.Language, "chrome_not_blank", nil)
		case versionErr != nil:
			chromeCheck.Detail = text(o.Language, "chrome_version_failed", map[string]any{"error": versionErr.Error()})
		default:
			chromeCheck.Passed, chromeCheck.Detail, chromeCheck.Action = true, text(o.Language, "chrome_started", nil), ""
			chromeCheck.Values = map[string]any{"version": version, "url": "about:blank", "sandbox": true, "webgl": o.WebGL}
		}
	}
	checks = append(checks, chromeCheck)
	view, err = remote.StartWithLog(ctx, display.Name(), outputs["x11vnc"])
	remoteCheck := Check{ID: "remote", Action: text(o.Language, "remote_remedy", nil)}
	if err != nil {
		remoteCheck.Detail = text(o.Language, "remote_failed", map[string]any{"error": err.Error()})
	} else {
		remoteCheck.Passed, remoteCheck.Detail, remoteCheck.Action = true, text(o.Language, "remote_started", nil), ""
	}
	checks = append(checks, remoteCheck)
	if err := logs.Err(); err != nil {
		checks = append(checks, Check{ID: "browser_logs", Detail: text(o.Language, "browser_logs_failed", map[string]any{"error": err.Error()}), Action: text(o.Language, "browser_logs_remedy", nil)})
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

func temporaryAccountID(language locales.Language) (int, error) {
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
	return 0, errors.New(text(language, "port_unavailable", nil))
}
