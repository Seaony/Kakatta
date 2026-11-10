package browser

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestDisplayRequiresLinuxContainer(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("非 Linux 平台的错误提示")
	}
	if d, err := StartDisplay(context.Background(), 1); d != nil || err == nil || !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("非 Linux 不能启动虚拟屏幕：%v %v", d, err)
	}
}
