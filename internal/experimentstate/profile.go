package experimentstate

import (
	"path/filepath"
	"strconv"
)

func ProfileDir(dataDir string, account int, fixture bool) string {
	branch := "experiment"
	if fixture {
		branch = "experiment-fixture"
	}
	return filepath.Join(dataDir, branch, strconv.Itoa(account), "browser")
}
