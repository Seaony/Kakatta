package remote

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVNCStartupFailureKeepsProcessOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x11vnc"), []byte("#!/bin/sh\nprintf 'local-vnc-startup-error\\n' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	if view, err := StartWithLog(t.Context(), ":10000", &output); view != nil || err == nil {
		t.Fatal("故障远程画面被当成启动成功")
	}
	if !bytes.Contains(output.Bytes(), []byte("local-vnc-startup-error")) {
		t.Fatal("启动失败前的标准错误输出丢失", output.String())
	}
}
