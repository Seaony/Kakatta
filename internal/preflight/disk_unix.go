//go:build !windows

package preflight

import (
	"github.com/Seaony/Kakatta/internal/locales"
	"golang.org/x/sys/unix"
)

func disk(dir string, language locales.Language) Check {
	var state unix.Statfs_t
	err := unix.Statfs(dir, &state)
	return diskResult(uint64(state.Bavail)*uint64(state.Bsize), err, language)
}
