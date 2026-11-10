//go:build linux

package service

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func platformMemory() (*int64, *int64) { return linuxMemory(readStatusFile) }

func readStatusFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("系统资源信息读取失败")
	}
	return data, nil
}

func linuxMemory(read func(string) ([]byte, error)) (*int64, *int64) {
	host, _ := read("/proc/meminfo")
	total, used := hostMemory(host)
	groups, _ := read("/proc/self/cgroup")
	var limit *int64
	var usagePath string
	for _, directory := range memoryDirectories(groups) {
		limitName, usageName := "memory.max", "memory.current"
		if directory.v1 {
			limitName, usageName = "memory.limit_in_bytes", "memory.usage_in_bytes"
		}
		raw, err := read(filepath.Join(directory.path, limitName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil
		}
		text := strings.TrimSpace(string(raw))
		if !directory.v1 && text == "max" {
			continue
		}
		value, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return nil, nil
		}
		// v1 的默认值是按页取整的最大计数，不是机器真的有这么多内存。
		if directory.v1 && value >= uint64(math.MaxInt64)-uint64(os.Getpagesize()-1) {
			continue
		}
		if value > math.MaxInt64 {
			return nil, nil
		}
		if total != nil && value >= uint64(*total) {
			continue
		}
		if limit == nil || int64(value) < *limit {
			value := int64(value)
			limit, usagePath = &value, filepath.Join(directory.path, usageName)
		}
	}
	if limit == nil {
		return total, used
	}
	// 容器用量必须和采用的限额来自同一层，不能拼上主机用量。
	raw, err := read(usagePath)
	if err != nil {
		return limit, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value < 0 {
		return limit, nil
	}
	return limit, &value
}

func hostMemory(raw []byte) (*int64, *int64) {
	values := map[string]int64{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" || fields[0] != "MemTotal:" && fields[0] != "MemAvailable:" {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && value >= 0 && value <= math.MaxInt64/1024 {
			values[fields[0]] = value * 1024
		}
	}
	total, known := values["MemTotal:"]
	if !known || total == 0 {
		return nil, nil
	}
	available, known := values["MemAvailable:"]
	if !known || available > total {
		return &total, nil
	}
	used := total - available
	return &total, &used
}

type memoryDirectory struct {
	path string
	v1   bool
}

func memoryDirectories(raw []byte) []memoryDirectory {
	result := []memoryDirectory{}
	seen := map[string]bool{}
	add := func(root, group string, v1 bool) {
		if !strings.HasPrefix(group, "/") {
			return
		}
		for _, part := range strings.Split(group, "/") {
			if part == ".." {
				return
			}
		}
		path := filepath.Join(root, group)
		for {
			if !seen[path] {
				result = append(result, memoryDirectory{path, v1})
				seen[path] = true
			}
			if path == root {
				break
			}
			path = filepath.Dir(path)
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			add("/sys/fs/cgroup", parts[2], false)
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				add("/sys/fs/cgroup/memory", parts[2], true)
				break
			}
		}
	}
	// 容器中的 cgroup 命名空间常把当前组直接挂成根目录。
	add("/sys/fs/cgroup", "/", false)
	add("/sys/fs/cgroup/memory", "/", true)
	return result
}
