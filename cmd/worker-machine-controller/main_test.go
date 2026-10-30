package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/scrape"
)

func TestAPIVersionAtLeast(t *testing.T) {
	tests := []struct {
		version, min string
		want         bool
	}{
		{"1.55", "1.44", true},
		{"1.44", "1.44", true},
		{"1.43", "1.44", false},
		{"2.0", "1.44", true},
		{"1.9", "1.44", false},
		{"", "1.44", false},
		{"x.y", "1.44", false},
	}
	for _, tt := range tests {
		if got := apiVersionAtLeast(tt.version, tt.min); got != tt.want {
			t.Errorf("apiVersionAtLeast(%q, %q) = %v, want %v", tt.version, tt.min, got, tt.want)
		}
	}
}

type fakeChecker struct {
	version string
	pingErr error
	info    docker.Info
}

func (f fakeChecker) Ping(context.Context) (string, error)      { return f.version, f.pingErr }
func (f fakeChecker) Info(context.Context) (docker.Info, error) { return f.info, nil }

func TestCheckDocker(t *testing.T) {
	ctx := context.Background()
	info, err := checkDocker(ctx, fakeChecker{version: "1.55", info: docker.Info{ServerVersion: "29.6.1"}})
	if err != nil || info.ServerVersion != "29.6.1" || info.APIVersion != "1.55" {
		t.Errorf("checkDocker = %+v, %v", info, err)
	}
	if _, err := checkDocker(ctx, fakeChecker{version: "1.41"}); err == nil {
		t.Error("an old API version was accepted")
	}
	if _, err := checkDocker(ctx, fakeChecker{pingErr: errors.New("no daemon")}); err == nil {
		t.Error("a daemon that does not answer was accepted")
	}
}

type fakeWorkers []discovery.Container

func (f fakeWorkers) Workers() []discovery.Container { return f }

type recordingSyncer struct {
	mu      sync.Mutex
	running []string
	calls   int
}

func (r *recordingSyncer) Sync(_ context.Context, running []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running, r.calls = running, r.calls+1
}

type recordingScraper struct {
	mu      sync.Mutex
	targets []scrape.Target
}

func (r *recordingScraper) ScrapeAll(_ context.Context, targets []scrape.Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targets = targets
}

func TestObserveSyncsAndScrapesTheRunningWorkers(t *testing.T) {
	workers := fakeWorkers{
		{ContainerInfo: docker.ContainerInfo{ID: "c1", State: "running"}, TelemetryURL: "http://172.20.0.5:9464"},
		{ContainerInfo: docker.ContainerInfo{ID: "c2", State: "running"}},
		{ContainerInfo: docker.ContainerInfo{ID: "c3", State: "exited"}, TelemetryURL: "http://172.20.0.6:9464"},
	}
	s, sc := &recordingSyncer{}, &recordingScraper{}

	observe(context.Background(), workers, s, sc)

	if strings.Join(s.running, ",") != "c1,c2" {
		t.Errorf("synced %v, want the running containers c1 and c2", s.running)
	}
	if len(sc.targets) != 1 || sc.targets[0] != (scrape.Target{ContainerID: "c1", URL: "http://172.20.0.5:9464"}) {
		t.Errorf("targets = %+v, want only the running one with a URL", sc.targets)
	}
}

func TestObserveEveryRunsAtOnceAndStops(t *testing.T) {
	s := &recordingSyncer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		observeEvery(ctx, 10*time.Millisecond, fakeWorkers{}, s, &recordingScraper{})
		close(done)
	}()
	time.Sleep(35 * time.Millisecond)
	cancel()
	<-done

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls < 2 {
		t.Errorf("observe ran %d times, want it at once and on the ticks", s.calls)
	}
}

func TestNewLoggerCarriesTheHost(t *testing.T) {
	var buf bytes.Buffer
	newLogger("json", "host1", &buf).Info("listening")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil || rec["host"] != "host1" {
		t.Errorf("record = %s", buf.String())
	}
	buf.Reset()
	newLogger("text", "host1", &buf).Info("listening")
	if !strings.Contains(buf.String(), "host=host1") {
		t.Errorf("record = %s", buf.String())
	}
}
