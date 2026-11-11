//go:build windows

package preflight

import (
	"github.com/Seaony/Kakatta/internal/locales"
	"golang.org/x/sys/windows"
)

func disk(dir string, language locales.Language) Check {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return diskResult(0, err, language)
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(path, &available, nil, nil)
	return diskResult(available, err, language)
}
