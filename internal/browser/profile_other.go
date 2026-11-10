//go:build !linux && !windows

package browser

import (
	"os/exec"
	"strings"
)

func profileInUse(dir string) (bool, error) {
	b, err := exec.Command("ps", "-A", "-o", "args=").Output()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		// ps 不保留参数边界，匹配路径末尾才能避免误认相近目录。
		needle := "--user-data-dir=" + dir
		if i := strings.Index(line, needle); i >= 0 && (i+len(needle) == len(line) || line[i+len(needle)] == ' ') {
			return true, nil
		}
	}
	return false, nil
}
