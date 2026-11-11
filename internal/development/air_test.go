//go:build kakatta_dev

package development

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func TestAirPublishesCandidatesAndWaitsForRunningBackend(t *testing.T) {
	air := os.Getenv("KAKATTA_DEV_AIR")
	if air == "" {
		t.Skip("设置 KAKATTA_DEV_AIR 执行真实编译监听回归")
	}
	projectRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	manager := filepath.Join(dir, "devhost")
	build := exec.Command("go", "build", "-tags", "kakatta_dev", "-o", manager, "./cmd/devhost")
	build.Dir = projectRoot
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("管理器编译失败：%s %v", raw, err)
	}
	project := filepath.Join(dir, "project")
	source := filepath.Join(project, "cmd/kakatta/main.go")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module local-test\n\ngo 1.27.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(developmentBackend), 0600); err != nil {
		t.Fatal(err)
	}
	state, data := filepath.Join(dir, "state"), filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	listen, backend := availableAddress(t), availableAddress(t)
	logPath := filepath.Join(dir, "manager.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	hold, entered := filepath.Join(data, "hold-build"), filepath.Join(data, "build-entered")
	wrapper := "#!/bin/sh\nif [ -f " + shellQuote(hold) + " ]; then\ntouch " + shellQuote(entered) + "\nwhile [ -f " + shellQuote(hold) + " ]; do sleep 0.05; done\nfi\nexec " + shellQuote(realGo) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	managerWrapper := filepath.Join(dir, "run-manager")
	wrapper = "#!/bin/sh\nexport PATH=" + shellQuote(bin) + ":\"$PATH\"\nexec " + shellQuote(manager) + " \"$@\"\n"
	if err := os.WriteFile(managerWrapper, []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	process, err := startChild(managerWrapper, []string{"--mode", "real", "--root", project, "--state", state, "--data", data, "--listen", listen, "--backend-listen", backend, "--air", air, "--poll"}, project, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(hold)
		process.interrupt()
		select {
		case <-process.done:
		case <-time.After(8 * time.Second):
			process.cmd.Process.Kill()
			<-process.done
		}
		if t.Failed() {
			raw, _ := os.ReadFile(logPath)
			t.Log(string(raw))
		}
	})
	read := func() Status {
		req, _ := http.NewRequest(http.MethodGet, "http://"+listen+"/__dev/status", nil)
		req.Host = "127.0.0.1:18081"
		client := http.Client{Timeout: time.Second}
		response, err := client.Do(req)
		if err != nil {
			return Status{}
		}
		defer response.Body.Close()
		var status Status
		_ = json.NewDecoder(response.Body).Decode(&status)
		return status
	}
	wait := func(condition func(Status) bool) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if condition(read()) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("编译监听没有到达预期状态：%+v", read())
	}
	wait(func(status Status) bool { return status.Phase == "running" && status.Generation == 1 })
	eventsPath := filepath.Join(data, "events")
	eventsBefore, _ := os.ReadFile(eventsPath)
	if err := os.WriteFile(source, []byte(developmentBackend+"\ninvalid Go source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wait(func(status Status) bool { return status.Phase == "reload_failed" })
	if _, err := os.Stat(filepath.Join(state, "build-errors.log")); err != nil {
		t.Fatalf("编译错误没有保存到管理目录：%v", err)
	}
	eventsFailed, _ := os.ReadFile(eventsPath)
	if string(eventsFailed) != string(eventsBefore) {
		t.Fatal("真实编译失败终止了原后端")
	}
	if err := os.WriteFile(filepath.Join(data, "busy"), []byte("当前运行"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(strings.ReplaceAll(developmentBackend, "version-one", "version-two")), 0600); err != nil {
		t.Fatal(err)
	}
	wait(func(status Status) bool { return status.Phase == "reloading" })
	eventsWaiting, _ := os.ReadFile(eventsPath)
	if strings.Contains(string(eventsWaiting), "interrupt") {
		t.Fatal("真实监听在排空前终止了后端")
	}
	if err := os.Remove(filepath.Join(data, "busy")); err != nil {
		t.Fatal(err)
	}
	wait(func(status Status) bool { return status.Phase == "running" && status.Generation == 2 })
	status := read()
	for _, line := range status.Logs {
		if strings.Contains(line.Text, "hidden-test") {
			t.Fatal("真实子进程日志没有脱敏")
		}
	}
	var previousBuild buildProcess
	rawPrevious, _ := os.ReadFile(filepath.Join(state, "build-process.json"))
	if err := json.Unmarshal(rawPrevious, &previousBuild); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hold, []byte("编译未结束"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(strings.ReplaceAll(developmentBackend, "version-one", "version-three")), 0600); err != nil {
		t.Fatal(err)
	}
	wait(func(Status) bool { _, err := os.Stat(entered); return err == nil })
	var compiling buildProcess
	wait(func(Status) bool {
		raw, _ := os.ReadFile(filepath.Join(state, "build-process.json"))
		return json.Unmarshal(raw, &compiling) == nil && compiling.GoPID > 0 && compiling.GoPID != previousBuild.GoPID
	})
	if err := process.interrupt(); err != nil {
		t.Fatal(err)
	}
	wait(func(Status) bool { _, err := os.Stat(filepath.Join(state, "stop-build")); return err == nil })
	select {
	case <-process.done:
		t.Fatal("尚在编译时管理器已经退出")
	default:
	}
	if syscall.Kill(compiling.HelperPID, 0) != nil || syscall.Kill(compiling.GoPID, 0) != nil {
		t.Fatal("编译中退出回归没有覆盖仍存活的编译进程")
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
		if process.err != nil {
			t.Fatal(process.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("真实编译监听没有随管理器退出")
	}
	for _, address := range []string{listen, backend} {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			t.Fatalf("退出后端口仍被占用：%s", address)
		}
	}
	for _, pid := range []int{compiling.HelperPID, compiling.GoPID} {
		waitFor(t, func() bool { return syscall.Kill(pid, 0) != nil })
		t.Logf("已确认退出：PID %d", pid)
	}
}

const developmentBackend = `package main
import("encoding/json";"flag";"fmt";"net/http";"os";"os/signal";"path/filepath";"time")
func main(){
 flags:=flag.NewFlagSet("dev",flag.ExitOnError)
 data:=flags.String("data","","");listen:=flags.String("listen","","")
 flags.String("mode","","");flags.String("control-token-file","","");flags.String("fixture","","")
 flags.Parse(os.Args[2:])
 events,_:=os.OpenFile(filepath.Join(*data,"events"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600)
 fmt.Fprintln(events,"start version-one",os.Getpid(),os.Getppid())
 fmt.Println("API_KEY=hidden-test")
 mux:=http.NewServeMux()
 mux.HandleFunc("GET /__dev/backend/status",func(w http.ResponseWriter,r *http.Request){json.NewEncoder(w).Encode(map[string]any{"scheduler_paused":true,"reload_wait_ms":5000})})
 mux.HandleFunc("POST /__dev/backend/prepare-reload",func(w http.ResponseWriter,r *http.Request){
  for{if _,err:=os.Stat(filepath.Join(*data,"busy"));os.IsNotExist(err){break};select{case<-r.Context().Done():return;case<-time.After(10*time.Millisecond):}}
  json.NewEncoder(w).Encode(map[string]bool{"ready":true})
 })
 mux.HandleFunc("POST /__dev/backend/cancel-reload",func(w http.ResponseWriter,r *http.Request){json.NewEncoder(w).Encode(map[string]bool{"ok":true})})
 server:=http.Server{Addr:*listen,Handler:mux}
 go server.ListenAndServe()
 signals:=make(chan os.Signal,1);signal.Notify(signals,os.Interrupt);<-signals
 fmt.Fprintln(events,"interrupt version-one");server.Close();events.Close()
}
`
