// Package browser 负责启动和控制抓取用的 Chrome：用固定端口连接、读取页面自己发出的接口响应、模拟真人的点击和输入。
// 规则见 docs/CRAWL-POLICY.md 第二节。
package browser

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrNoChrome 表示这台电脑上找不到可用的 Chrome 或 Edge。
var ErrNoChrome = errors.New("找不到 Google Chrome 或 Microsoft Edge")

// FindChrome 返回本机已安装的 Google Chrome 路径；Windows 上没有 Chrome 时退而用 Edge。
// 不用 Chromium：它缺少 H.264/AAC 解码器，和普通 Chrome 不一样。
func FindChrome() (string, error) {
	for _, p := range chromeCandidates() {
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
			continue
		}
		if found, err := exec.LookPath(p); err == nil {
			return found, nil
		}
	}
	return "", ErrNoChrome
}

func chromeCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
		}
	case "windows":
		var list []string
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base != "" {
				list = append(list, filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
			}
		}
		for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
			if base != "" {
				list = append(list, filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
			}
		}
		return list
	default:
		return []string{"google-chrome-stable", "google-chrome"}
	}
}
