package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type coreSoakProcessMemory struct {
	PID      string  `json:"pid"`
	Role     string  `json:"role"`
	RSSBytes *uint64 `json:"rss_bytes"`
	PSSBytes *uint64 `json:"pss_bytes"`
}

type coreSoakProcessGroup struct {
	PIDs           int    `json:"pids"`
	RSSBytes       uint64 `json:"rss_bytes"`
	PSSBytes       uint64 `json:"pss_bytes"`
	RSSUnavailable int    `json:"rss_unavailable"`
	PSSUnavailable int    `json:"pss_unavailable"`
}

type coreSoakResourceSample struct {
	At                    string                          `json:"at"`
	Phase                 string                          `json:"phase"`
	Go                    map[string]uint64               `json:"go"`
	ServiceRSSBytes       *uint64                         `json:"service_rss_bytes"`
	ServiceFDs            *int                            `json:"service_fds"`
	CgroupCurrentBytes    *uint64                         `json:"cgroup_current_bytes"`
	CgroupPeakBytes       *uint64                         `json:"cgroup_peak_bytes"`
	CgroupMax             string                          `json:"cgroup_max"`
	CgroupMemoryStatBytes map[string]uint64               `json:"cgroup_memory_stat_bytes"`
	CgroupEvents          map[string]uint64               `json:"cgroup_events"`
	ProcessGroups         map[string]coreSoakProcessGroup `json:"process_groups"`
	Processes             []coreSoakProcessMemory         `json:"processes"`
	DatabaseFilesBytes    map[string]int64                `json:"database_files_bytes"`
	Unavailable           []string                        `json:"unavailable"`
}

func startCoreSoakMetrics(t *testing.T, h *coreFlowHarness, path string) func() {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(path, "resource-samples.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(coreSoakResources(h.paths.Database)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var writeErr error
	go func() {
		defer close(done)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if writeErr = encoder.Encode(coreSoakResources(h.paths.Database)); writeErr != nil {
					return
				}
			}
		}
	}()
	finish := sync.OnceFunc(func() {
		close(stop)
		<-done
		writeErr = errors.Join(writeErr, file.Close())
		data, err := json.MarshalIndent(coreSoakResources(h.paths.Database), "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(path, "finish.json"), append(data, '\n'), 0600)
		}
		if err = errors.Join(writeErr, err); err != nil {
			t.Errorf("长跑资源记录保存失败：%v", err)
		}
	})
	t.Cleanup(finish)
	return finish
}

func coreSoakResources(database string) coreSoakResourceSample {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	sample := coreSoakResourceSample{
		At: time.Now().UTC().Format(time.RFC3339Nano), Phase: "released",
		Go: map[string]uint64{"heap_alloc_bytes": memory.HeapAlloc, "heap_inuse_bytes": memory.HeapInuse,
			"heap_objects": memory.HeapObjects, "sys_bytes": memory.Sys, "gc_count": uint64(memory.NumGC), "goroutines": uint64(runtime.NumGoroutine())},
		ProcessGroups:      make(map[string]coreSoakProcessGroup),
		Processes:          []coreSoakProcessMemory{},
		DatabaseFilesBytes: make(map[string]int64), Unavailable: []string{},
	}
	read := func(path, label string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			sample.Unavailable = append(sample.Unavailable, label)
		}
		return data
	}
	readUint := func(name string) *uint64 {
		data := read(filepath.Join("/sys/fs/cgroup", name), name)
		value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			if data != nil {
				sample.Unavailable = append(sample.Unavailable, name+".number")
			}
			return nil
		}
		return &value
	}
	sample.CgroupCurrentBytes = readUint("memory.current")
	sample.CgroupPeakBytes = readUint("memory.peak")
	sample.CgroupMax = strings.TrimSpace(string(read("/sys/fs/cgroup/memory.max", "memory.max")))
	sample.CgroupEvents = coreSoakCounters(read("/sys/fs/cgroup/memory.events", "memory.events"))
	stat := coreSoakCounters(read("/sys/fs/cgroup/memory.stat", "memory.stat"))
	sample.CgroupMemoryStatBytes = make(map[string]uint64)
	for _, key := range []string{"anon", "file", "kernel", "shmem"} {
		if value, exists := stat[key]; exists {
			sample.CgroupMemoryStatBytes[key] = value
		} else {
			sample.Unavailable = append(sample.Unavailable, "memory.stat."+key)
		}
	}
	sample.ServiceRSSBytes = coreSoakKiBField(read("/proc/self/status", "service.status"), "VmRSS:")
	if descriptors, err := os.ReadDir("/proc/self/fd"); err == nil {
		count := len(descriptors)
		sample.ServiceFDs = &count
	} else {
		sample.Unavailable = append(sample.Unavailable, "service.fds")
	}
	processes, err := filepath.Glob("/proc/[0-9]*/comm")
	if err != nil || len(processes) == 0 {
		sample.Unavailable = append(sample.Unavailable, "processes")
	}
	for _, process := range processes {
		name, err := os.ReadFile(process)
		if err != nil {
			continue
		}
		directory := filepath.Dir(process)
		pid := filepath.Base(directory)
		role := "other"
		switch {
		case pid == strconv.Itoa(os.Getpid()):
			role = "service"
		case strings.HasPrefix(strings.TrimSpace(string(name)), "chrome"), strings.HasPrefix(strings.TrimSpace(string(name)), "google-chrome"):
			role = "chrome"
		case strings.TrimSpace(string(name)) == "Xvfb":
			role = "display"
		case strings.TrimSpace(string(name)) == "x11vnc":
			role = "remote"
		}
		status, _ := os.ReadFile(filepath.Join(directory, "status"))
		rollup, _ := os.ReadFile(filepath.Join(directory, "smaps_rollup"))
		rss, pss := coreSoakKiBField(status, "VmRSS:"), coreSoakKiBField(rollup, "Pss:")
		// 退出中的进程允许读不到；缺失数单独保存，不能冒充零内存。
		if _, err := os.Stat(directory); os.IsNotExist(err) {
			continue
		}
		sample.Processes = append(sample.Processes, coreSoakProcessMemory{PID: pid, Role: role, RSSBytes: rss, PSSBytes: pss})
		group := sample.ProcessGroups[role]
		group.PIDs++
		if rss == nil {
			group.RSSUnavailable++
		} else {
			group.RSSBytes += *rss
		}
		if pss == nil {
			group.PSSUnavailable++
		} else {
			group.PSSBytes += *pss
		}
		sample.ProcessGroups[role] = group
		if role == "chrome" || role == "display" || role == "remote" {
			sample.Phase = "running"
		}
	}
	for label, suffix := range map[string]string{"database": "", "wal": "-wal", "shm": "-shm"} {
		info, err := os.Stat(database + suffix)
		if err == nil {
			sample.DatabaseFilesBytes[label] = info.Size()
		} else if os.IsNotExist(err) {
			sample.DatabaseFilesBytes[label] = 0
		} else {
			sample.Unavailable = append(sample.Unavailable, "database."+label)
		}
	}
	return sample
}

func coreSoakCounters(data []byte) map[string]uint64 {
	values := make(map[string]uint64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			if value, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				values[fields[0]] = value
			}
		}
	}
	return values
}

func coreSoakKiBField(data []byte, name string) *uint64 {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == name && fields[2] == "kB" {
			if value, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				bytes := value * 1024
				return &bytes
			}
		}
	}
	return nil
}

func coreSoakBrowserProcesses() ([]string, error) {
	processes, err := filepath.Glob("/proc/[0-9]*/comm")
	if err != nil {
		return nil, err
	}
	remaining := []string{}
	for _, path := range processes {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(string(data))
		if strings.HasPrefix(name, "chrome") || strings.HasPrefix(name, "google-chrome") || name == "Xvfb" || name == "x11vnc" {
			remaining = append(remaining, filepath.Base(filepath.Dir(path))+" "+name)
		}
	}
	return remaining, nil
}

func TestCoreSoakMetricParsersKeepCountersAndUnavailableValues(t *testing.T) {
	got := coreSoakCounters([]byte("anon 128\nfile 256\noom_kill 0\nwrong nope\nempty\n"))
	if len(got) != 3 || got["anon"] != 128 || got["file"] != 256 || got["oom_kill"] != 0 {
		t.Fatalf("cgroup 原始计数解析错误：%v", got)
	}
	for _, name := range []string{"VmRSS:", "Pss:"} {
		data := []byte(fmt.Sprintf("%s\t 123 kB\nPss_Dirty: 7 kB\n", name))
		if value := coreSoakKiBField(data, name); value == nil || *value != 123*1024 {
			t.Fatalf("%s没有准确换成字节：%v", name, value)
		}
		if value := coreSoakKiBField([]byte("unavailable"), name); value != nil {
			t.Fatal("缺失指标被当成零内存")
		}
	}
}

func TestCoreSoakMetricsStopsAndKeepsTheFinalSnapshot(t *testing.T) {
	h := &coreFlowHarness{}
	h.paths.Database = filepath.Join(t.TempDir(), "database.db")
	if err := os.WriteFile(h.paths.Database, []byte("database"), 0600); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	stop := startCoreSoakMetrics(t, h, path)
	stop()
	before, err := os.ReadFile(filepath.Join(path, "resource-samples.jsonl"))
	if err != nil || len(before) == 0 {
		t.Fatalf("没有保存立即采样：%v", err)
	}
	stop()
	after, err := os.ReadFile(filepath.Join(path, "resource-samples.jsonl"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("停止后再次调用改变了已结束的记录：%v", err)
	}
	data, err := os.ReadFile(filepath.Join(path, "finish.json"))
	var final coreSoakResourceSample
	if err != nil || json.Unmarshal(data, &final) != nil || final.At == "" || final.Go["heap_objects"] == 0 || final.DatabaseFilesBytes["database"] != 8 || final.DatabaseFilesBytes["wal"] != 0 || final.DatabaseFilesBytes["shm"] != 0 {
		t.Fatalf("最终快照缺失、格式错误或文件大小不符：%s，%v", data, err)
	}
}
