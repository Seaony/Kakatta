package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/buildinfo"
	"github.com/Seaony/Kakatta/internal/config"
)

type formalServerLifecycle struct {
	Event                string   `json:"event"`
	At                   string   `json:"at"`
	Instance             string   `json:"instance"`
	PID                  int      `json:"pid"`
	Commit               string   `json:"commit"`
	Stage                string   `json:"stage"`
	Success              *bool    `json:"success,omitempty"`
	ProcessCleanupPassed *bool    `json:"process_cleanup_passed,omitempty"`
	RemainingProcesses   []string `json:"remaining_processes,omitempty"`
}

type formalServiceWriter struct {
	mu              sync.Mutex
	log, lifecycle  *os.File
	instance, stage string
	started         bool
	err             error
}

func (w *formalServiceWriter) appendLifecycle(event string, success, cleaned *bool, remaining []string) error {
	entry := formalServerLifecycle{Event: event, At: time.Now().UTC().Format(time.RFC3339Nano), Instance: w.instance, PID: os.Getpid(), Commit: buildinfo.Commit, Stage: w.stage, Success: success, ProcessCleanupPassed: cleaned, RemainingProcesses: remaining}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = w.lifecycle.Write(append(data, '\n'))
	return err
}

func (w *formalServiceWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.log.Write(data)
	if err != nil {
		w.err = errors.Join(w.err, err)
		return n, err
	}
	var entry struct {
		Message string `json:"msg"`
	}
	if !w.started && json.Unmarshal(data, &entry) == nil && entry.Message == "服务已启动" {
		w.started = true
		w.err = errors.Join(w.err, w.appendLifecycle("started", nil, nil, nil))
	}
	return n, nil
}

func formalServerRemainingProcesses() []string {
	result := []string{}
	paths, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(data))
		if strings.HasPrefix(name, "chrome") || strings.HasPrefix(name, "google-chrome") || name == "Xvfb" || name == "x11vnc" {
			result = append(result, filepath.Base(filepath.Dir(path))+" "+name)
		}
	}
	return result
}

func TestFormalPathServer(t *testing.T) {
	stage := requireFormalPath(t)
	metadata := loadFormalMetadata(t)
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	work := os.Getenv("KAKATTA_FORMAL_DIR")
	logFile, err := os.OpenFile(filepath.Join(work, "service.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	lifecycle, err := os.OpenFile(filepath.Join(work, "server-lifecycle.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	writer := &formalServiceWriter{log: logFile, lifecycle: lifecycle, instance: rand.Text(), stage: stage}
	cfg, err := config.Load([]string{"--data", os.Getenv("KAKATTA_FORMAL_DATA"), "--listen", "127.0.0.1:18080"}, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	factory := func(ctx context.Context, cfg config.Config, paths config.Paths, key [backup.MasterKeyBytes]byte, source backup.KeySource, generation string) (*runtimeGraph, error) {
		defer clear(key[:])
		return newRuntimeGraphWithSite(ctx, cfg, paths, key, source, generation, metadata.Site)
	}
	err = serveWithGraphFactory(ctx, cfg, os.LookupEnv, slog.New(slog.NewJSONHandler(writer, nil)), factory)
	deadline := time.Now().Add(5 * time.Second)
	remaining := formalServerRemainingProcesses()
	for len(remaining) > 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		remaining = formalServerRemainingProcesses()
	}
	cleaned := len(remaining) == 0
	success := err == nil && writer.err == nil && cleaned
	if appendErr := writer.appendLifecycle("stopped", &success, &cleaned, remaining); appendErr != nil {
		t.Error(appendErr)
	}
	if syncErr := lifecycle.Sync(); syncErr != nil {
		t.Error(syncErr)
	}
	if syncErr := logFile.Sync(); syncErr != nil {
		t.Error(syncErr)
	}
	if err != nil {
		t.Error(err)
	}
	if writer.err != nil {
		t.Error(writer.err)
	}
	if !cleaned {
		t.Fatal("正式服务停止后仍有浏览器或远程画面进程", remaining)
	}
}
