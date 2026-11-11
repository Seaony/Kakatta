//go:build windows

package preflight

import (
	"golang.org/x/sys/windows"
)

func disk(dir string) Check {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return diskResult(0, err)
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(path, &available, nil, nil)
	return diskResult(available, err)
}
