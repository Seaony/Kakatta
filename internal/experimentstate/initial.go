package experimentstate

import (
	"os"
	"path/filepath"
	"regexp"
)

var initialEventName = regexp.MustCompile(`^events-[0-9]{4}-[0-9]{2}-[0-9]{2}\.jsonl$`)
var initialProcessName = regexp.MustCompile(`^(chrome|xvfb|x11vnc)\.log(\.1)?$`)
var initialSummaryName = regexp.MustCompile(`^summary-[0-9]{8}-[0-9]{6}-[0-9]+$`)

// InitialLogsOnly 区分首次自检或设置失败的诊断目录和丢了保护记录的旧账号。
func InitialLogsOnly(dir string, profiles ...string) bool {
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	for _, profile := range profiles {
		if _, err := os.Lstat(profile); !os.IsNotExist(err) {
			return false
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	hasLogs := false
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		switch entry.Name() {
		case "lock":
			if !info.Mode().IsRegular() {
				return false
			}
		case "logs":
			if !info.IsDir() || !initialLogs(filepath.Join(dir, "logs")) {
				return false
			}
			hasLogs = true
		default:
			return false
		}
	}
	return hasLogs
}

func initialLogs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if info.Mode().IsRegular() && (entry.Name() == "sequence.json" || initialEventName.MatchString(entry.Name()) || initialProcessName.MatchString(entry.Name())) {
			continue
		}
		if entry.Name() != "summaries" || !info.IsDir() || !initialSummaries(path) {
			return false
		}
	}
	return true
}

func initialSummaries(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !initialSummaryName.MatchString(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return false
		}
		files, err := os.ReadDir(filepath.Join(dir, entry.Name()))
		if err != nil || len(files) != 1 || files[0].Name() != "summary.json" || !files[0].Type().IsRegular() {
			return false
		}
	}
	return true
}
