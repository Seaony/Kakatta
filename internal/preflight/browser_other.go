//go:build !linux

package preflight

import "context"

func browserChecks(_ context.Context, _ Options) []Check {
	var checks []Check
	for _, id := range []string{"display", "chrome", "remote", "cleanup"} {
		checks = append(checks, Check{ID: id, Detail: "浏览器和远程画面自检需要 Linux 容器", Action: "在正式 Docker 镜像内运行同一条自检命令"})
	}
	return checks
}
