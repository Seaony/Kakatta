package preflight

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTemporaryAccountIDSkipsOccupiedPort(t *testing.T) {
	first, err := temporaryAccountID()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(20000+first))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	next, err := temporaryAccountID()
	if err != nil || next == first {
		t.Fatalf("自检复用了已占用的浏览器端口：%d，%v", next, err)
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
