package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	newLogger("", &buf).Info("listening")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil || rec["msg"] != "listening" {
		t.Errorf("default record = %q, want JSON", buf.String())
	}
	buf.Reset()
	newLogger("text", &buf).Info("listening")
	if strings.HasPrefix(buf.String(), "{") || !strings.Contains(buf.String(), "msg=listening") {
		t.Errorf("text record = %q", buf.String())
	}
}

// The binary starts with a configuration, serves /healthz and the UI, and exits 0 on SIGTERM.
func TestTheBinaryStartsAndStops(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "workers-control-plane")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	cfg := fmt.Sprintf("listen: %q\ndatabase: %q\nhosts:\n  - name: host1\n    workerMachineControllerUrl: http://127.0.0.1:1\n    controlToken: ${TOKEN}\n",
		addr, filepath.Join(dir, "monitor.db"))
	cfgPath := filepath.Join(dir, "config.yml")
	os.WriteFile(cfgPath, []byte(cfg), 0o600)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MONITOR_CONFIG="+cfgPath, "TOKEN=0123456789abcdef0123456789abcdef", "LOG_FORMAT=text")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	var resp *http.Response
	var err error
	for range 50 {
		if resp, err = http.Get("http://" + addr + "/"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the control plane does not answer: %v\n%s", err, logs.String())
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET / = %d", resp.StatusCode)
	}
	// the only host does not answer: not healthy
	if resp, err := http.Get("http://" + addr + "/healthz"); err != nil || resp.StatusCode != 503 {
		t.Errorf("healthz = %v, %v, want 503", resp, err)
	}

	cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit: %v\n%s", err, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the control plane did not stop")
	}
}

func TestABadConfigurationExitsWithOne(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "workers-control-plane")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MONITOR_CONFIG="+filepath.Join(dir, "missing.yml"))
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Errorf("exit = %v, want 1", err)
	}
}
