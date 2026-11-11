package preflight

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Seaony/Kakatta/internal/locales"
)

func TestTemporaryAccountIDSkipsOccupiedPort(t *testing.T) {
	first, err := temporaryAccountID(locales.ZhCN)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(20000+first))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	next, err := temporaryAccountID(locales.ZhCN)
	if err != nil || next == first {
		t.Fatalf("自检复用了已占用的浏览器端口：%d，%v", next, err)
	}
}

func TestBrowserSetupFailureUsesSelectedLanguage(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, language := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		t.Run(string(language), func(t *testing.T) {
			checks := browserChecks(t.Context(), Options{DataDir: filepath.Join(file, "child"), Language: language})
			if len(checks) != 4 {
				t.Fatalf("创建目录失败后检查项不全：%+v", checks)
			}
			for _, check := range checks[:3] {
				if check.Passed || check.Detail == "" || check.Action == "" || check.Detail != text(language, "profile_failed", nil) || check.Action != text(language, "mount_remedy", nil) {
					t.Errorf("浏览器启动前失败没有保留译文：%+v", check)
				}
			}
			if cleanup := checks[3]; cleanup.ID != "cleanup" || !cleanup.Passed || cleanup.Action != "" || cleanup.Detail == "" || cleanup.Detail != text(language, "processes_not_started", nil) {
				t.Errorf("未启动进程时清理说明不正确：%+v", cleanup)
			}
		})
	}
}

func TestPreflightBrowserLifecycle(t *testing.T) {
	if os.Getenv("KAKATTA_BROWSER_TESTS") != "1" {
		t.Skip("需要在断网的正式 Linux 镜像里启用浏览器自检")
	}
	dir := t.TempDir()
	checks := Run(t.Context(), Options{DataDir: dir, Timezone: "Asia/Shanghai", WebGL: "off"})
	for _, check := range checks {
		t.Logf("%s：%t，%s", check.ID, check.Passed, check.Detail)
		if !check.Passed {
			t.Errorf("自检未通过：%+v", check)
		}
		if check.ID == "chrome" {
			version, _ := check.Values["version"].(string)
			if check.Values["url"] != "about:blank" || !strings.HasPrefix(version, "Chrome/") {
				t.Fatalf("没有实际读取空白页和 Chrome 版本：%+v", check)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".preflight-") {
			t.Fatalf("自检没有回收临时浏览器目录：%s", entry.Name())
		}
	}
	for _, name := range []string{"chrome.log", "xvfb.log", "x11vnc.log"} {
		if _, err := os.Stat(filepath.Join(dir, "logs", name)); err != nil {
			t.Fatalf("缺少自检进程日志 %s：%v", name, err)
		}
	}
}
