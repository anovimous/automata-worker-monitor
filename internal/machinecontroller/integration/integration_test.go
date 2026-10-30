//go:build integration

// Package integration runs the worker machine controller against the Docker daemon of this machine, with the
// automata-fakeworker:dev image (make images). Every test removes the containers, volumes and network it created.
package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/api"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/hostmetrics"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/ports"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/scrape"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/stats"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/volumes"
)

const (
	image = "automata-fakeworker:dev"
	token = "integration-token-0123456789abcdef"
)

type stack struct {
	t      *testing.T
	url    string
	client *http.Client
}

// shared is the one controller of the tests, as on a host: two controllers would hand out the same ports.
var (
	shared     stack
	skipReason string
)

func TestMain(m *testing.M) {
	cleanup, err := start()
	if err != nil {
		skipReason = err.Error()
	}
	code := m.Run()
	if cleanup != nil {
		cleanup()
	}
	os.Exit(code)
}

// newStack is the shared controller, for one test.
func newStack(t *testing.T) *stack {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
	return &stack{t: t, url: shared.url, client: shared.client}
}

// start runs the controller's parts against the real daemon, faster than in production.
func start() (cleanup func(), err error) {
	engine, err := docker.New("")
	if err != nil {
		return nil, fmt.Errorf("no Docker client: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := engine.Ping(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("Docker is not reachable: %w", err)
	}
	if ok, _ := engine.ImageExists(ctx, image); !ok {
		cancel()
		return nil, fmt.Errorf("%s is missing, run make images", image)
	}
	info, _ := engine.Info(ctx)

	envDir, err := os.MkdirTemp("", "wmc-it-env-")
	if err != nil {
		cancel()
		return nil, err
	}
	os.WriteFile(filepath.Join(envDir, "default.env"), []byte("FAKE_ROUTINES=fuzz\n"), 0o600)
	cfg := config.Config{EnvDir: envDir, AllowedImages: []string{"automata-fakeworker:*"}}
	now := func() time.Time { return time.Now().UTC() }

	disc := discovery.New(engine)
	statsManager := stats.NewManager(engine)
	scraper := scrape.New(&http.Client{Timeout: scrape.Timeout}, now)
	host := hostmetrics.New(hostmetrics.Gopsutil{}, []string{"/"}, info.ServerVersion, now)
	vols := volumes.New(engine, now)
	prov := provision.New(engine, cfg, ports.New(config.PortRange{Min: 29100, Max: 29199}), disc, now)

	go disc.Run(ctx, 2*time.Second)
	go host.Run(ctx, time.Second)
	go vols.Run(ctx, 2*time.Second)
	go func() {
		for ctx.Err() == nil {
			var running []string
			var targets []scrape.Target
			for _, c := range disc.Workers() {
				if c.Running() {
					running = append(running, c.ID)
					if c.TelemetryURL != "" {
						targets = append(targets, scrape.Target{ContainerID: c.ID, URL: c.TelemetryURL})
					}
				}
			}
			statsManager.Sync(ctx, running)
			scraper.ScrapeAll(ctx, targets)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()

	srv := httptest.NewServer(api.NewHandler(api.Deps{
		HostName: "it", Version: "test", Token: token, EnvDir: envDir, DockerVersion: info.ServerVersion,
		DockerAPIVersion: info.APIVersion, Engine: engine, Workers: disc, Stats: statsManager, Scrapes: scraper,
		Volumes: vols, Host: host, Provisioner: prov, Now: now,
	}))
	shared = stack{url: srv.URL, client: &http.Client{Timeout: 90 * time.Second}}
	return func() {
		srv.Close()
		statsManager.Stop()
		cancel()
		engine.Close()
		os.RemoveAll(envDir)
	}, nil
}

func (s *stack) do(method, path string, body any) (int, []byte) {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.url+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func randomSuffix() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

// spec is a template for the fakeworker; env sets its scenario.
func spec(network string, env map[string]string) controllerapi.WorkerSpec {
	return controllerapi.WorkerSpec{
		Name: "it", Image: image, PullPolicy: controllerapi.PullNever, EnvFile: "default", Env: env, Network: network,
		CPUs: 2, MemoryBytes: 512 << 20, PidsLimit: 256, WordlistCacheMaxBytes: 64 << 20, TmpfsSocketsBytes: 1 << 20,
		RestartPolicy: controllerapi.RestartPolicy{Name: "no"}, StopTimeoutSeconds: 45,
	}
}

// create creates a worker and registers the removal of everything it leaves.
func (s *stack) create(network string, env map[string]string, restart controllerapi.RestartPolicy) string {
	s.t.Helper()
	workerID := "w-it-" + randomSuffix()
	sp := spec(network, env)
	if restart.Name != "" {
		sp.RestartPolicy = restart
	}
	s.t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", "automata-worker-"+workerID).Run()
		exec.Command("docker", "volume", "rm", "-f", "automata-wl-"+workerID, "automata-tmp-"+workerID).Run()
	})
	code, body := s.do("POST", "/v1/workers", controllerapi.CreateWorkerRequest{WorkerID: workerID, Template: sp})
	if code != http.StatusCreated {
		s.t.Fatalf("create = %d %s", code, body)
	}
	return workerID
}

func (s *stack) observation(workerID string) (controllerapi.WorkerObservation, bool) {
	s.t.Helper()
	_, body := s.do("GET", "/v1/snapshot", nil)
	var snap controllerapi.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		s.t.Fatalf("snapshot %s: %v", body, err)
	}
	for _, o := range snap.Workers {
		if o.WorkerID == workerID {
			return o, true
		}
	}
	return controllerapi.WorkerObservation{}, false
}

// waitFor polls the observation of a worker until cond holds.
func (s *stack) waitFor(workerID string, timeout time.Duration, what string, cond func(controllerapi.WorkerObservation) bool) controllerapi.WorkerObservation {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	var last controllerapi.WorkerObservation
	for time.Now().Before(deadline) {
		if o, ok := s.observation(workerID); ok {
			last = o
			if cond(o) {
				return o
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	b, _ := json.Marshal(last)
	s.t.Fatalf("timed out waiting for %s; last observation %s", what, b)
	return last
}

func telemetryOK(o controllerapi.WorkerObservation) bool {
	return o.Telemetry.OK && o.Telemetry.Status != nil
}

func volumeExists(name string) bool {
	return exec.Command("docker", "volume", "inspect", name).Run() == nil
}

func TestBridgeWorkerDrainLogsAndRemove(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	network := "automata-it-" + randomSuffix()
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })

	id := s.create(network, map[string]string{"FAKE_IDLE_SECONDS": "300"}, controllerapi.RestartPolicy{})
	o := s.waitFor(id, 30*time.Second, "the status and the stats", func(o controllerapi.WorkerObservation) bool {
		return telemetryOK(o) && o.Resources != nil && o.Volumes != nil
	})
	if o.Container.NetworkMode != network || !strings.HasSuffix(o.Telemetry.URL, ":9464") || o.Telemetry.Status.Phase != "IDLE" {
		t.Errorf("bridge worker = %+v", o)
	}
	if o.Resources.MemoryWorkingSetBytes <= 0 || o.Resources.Pids <= 0 || len(o.Volumes.Items) != 2 {
		t.Errorf("resources %+v volumes %+v", o.Resources, o.Volumes)
	}

	code, body := s.do("GET", "/v1/workers/"+id+"/logs?tail=50", nil)
	var lines []controllerapi.LogLine
	json.Unmarshal(body, &lines)
	if code != 200 || len(lines) == 0 || !strings.Contains(lines[0].Line, "fakeworker starting") {
		t.Errorf("logs = %d %s", code, body)
	}

	// idle: a drain exits at once, with 0
	if code, body := s.do("POST", "/v1/workers/"+id+"/signal", controllerapi.SignalRequest{Signal: "SIGUSR1"}); code != 200 {
		t.Fatalf("signal = %d %s", code, body)
	}
	s.waitFor(id, 20*time.Second, "the drained worker to exit with 0", func(o controllerapi.WorkerObservation) bool {
		return o.Container.State == "exited" && o.Container.ExitCode == 0 && o.Resources == nil
	})

	code, body = s.do("DELETE", "/v1/workers/"+id+"?removeVolumes=true", nil)
	var rm controllerapi.RemoveResponse
	json.Unmarshal(body, &rm)
	if code != 200 || len(rm.VolumesNotRemoved) != 0 {
		t.Fatalf("remove = %d %s", code, body)
	}
	if volumeExists("automata-wl-"+id) || volumeExists("automata-tmp-"+id) {
		t.Error("the volumes are still there")
	}
	if code, _ := s.do("GET", "/v1/workers/"+id, nil); code != 404 {
		t.Errorf("the removed worker answers %d, want 404", code)
	}
}

func TestHostWorkerStopStartKill(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	id := s.create("host", map[string]string{"FAKE_IDLE_SECONDS": "300"}, controllerapi.RestartPolicy{})

	o := s.waitFor(id, 30*time.Second, "the status through the loopback", telemetryOK)
	if !strings.HasPrefix(o.Telemetry.URL, "http://127.0.0.1:291") || o.Container.Labels["automata.worker.network-mode"] != "host" {
		t.Errorf("host worker telemetry %s labels %v", o.Telemetry.URL, o.Container.Labels)
	}

	start := time.Now()
	if code, body := s.do("POST", "/v1/workers/"+id+"/stop", controllerapi.StopRequest{TimeoutSeconds: 45}); code != 200 {
		t.Fatalf("stop = %d %s", code, body)
	}
	if o, _ := s.observation(id); o.Container.State != "exited" || o.Container.ExitCode != 0 || time.Since(start) > 10*time.Second {
		t.Errorf("after stop: %s %d after %s, want a quick exit with 0", o.Container.State, o.Container.ExitCode, time.Since(start))
	}

	if code, body := s.do("POST", "/v1/workers/"+id+"/start", nil); code != 200 {
		t.Fatalf("start = %d %s", code, body)
	}
	s.waitFor(id, 30*time.Second, "the status after the start", telemetryOK)

	if code, body := s.do("POST", "/v1/workers/"+id+"/kill", nil); code != 200 {
		t.Fatalf("kill = %d %s", code, body)
	}
	s.waitFor(id, 10*time.Second, "the exit code of the kill", func(o controllerapi.WorkerObservation) bool {
		return o.Container.State == "exited" && o.Container.ExitCode == 137
	})
}

func TestLogsFollow(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	id := s.create("host", map[string]string{"FAKE_IDLE_SECONDS": "1", "FAKE_JOB_SECONDS": "2"}, controllerapi.RestartPolicy{})
	s.waitFor(id, 30*time.Second, "the worker to run", telemetryOK)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.url+"/v1/workers/"+id+"/logs?follow=true&tail=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// the fake keeps taking jobs: new lines arrive while following
	sc := bufio.NewScanner(resp.Body)
	events := 0
	for sc.Scan() && events < 4 {
		if strings.HasPrefix(sc.Text(), "data: ") {
			events++
		}
	}
	if events < 4 {
		t.Errorf("got %d log events while following, want new lines", events)
	}
}

func TestCPUAndMemoryHogs(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	jobEnv := map[string]string{"FAKE_IDLE_SECONDS": "1", "FAKE_JOB_SECONDS": "300"}
	cpuEnv, memEnv := map[string]string{"FAKE_SCENARIO": "cpu-hog"}, map[string]string{"FAKE_SCENARIO": "mem-hog"}
	for k, v := range jobEnv {
		cpuEnv[k], memEnv[k] = v, v
	}
	cpu := s.create("host", cpuEnv, controllerapi.RestartPolicy{})
	mem := s.create("host", memEnv, controllerapi.RestartPolicy{})

	s.waitFor(cpu, 40*time.Second, "about one core in use", func(o controllerapi.WorkerObservation) bool {
		return o.Resources != nil && o.Resources.CPUCores > 0.8
	})
	s.waitFor(mem, 40*time.Second, "the memory to rise towards 80 % of 512 MiB", func(o controllerapi.WorkerObservation) bool {
		return o.Resources != nil && o.Resources.MemoryWorkingSetBytes > 200<<20 && o.Resources.MemoryLimitBytes == 512<<20
	})
}

func TestACrashingWorkerIsRestarted(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	id := s.create("host", map[string]string{"FAKE_SCENARIO": "crash"}, controllerapi.RestartPolicy{Name: "on-failure", MaxRetries: 5})

	o := s.waitFor(id, 70*time.Second, "a restart after the crash", func(o controllerapi.WorkerObservation) bool {
		return o.Container.RestartCount >= 1
	})
	if o.Container.Labels["automata.worker.id"] != id {
		t.Errorf("labels = %v", o.Container.Labels)
	}
	fmt.Fprintf(os.Stderr, "restart count %d, last exit code %d\n", o.Container.RestartCount, o.Container.ExitCode)
}
