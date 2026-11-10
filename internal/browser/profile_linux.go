package browser

import (
	"os"
	"path/filepath"
	"strings"
)

func profileInUse(dir string) (bool, error) {
	entries, err := filepath.Glob("/proc/[0-9]*/cmdline")
	if err != nil {
		return false, err
	}
	for _, name := range entries {
		b, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if commandUsesProfile(strings.Split(string(b), "\x00"), dir) {
			return true, nil
		}
	}
	return false, nil
}
