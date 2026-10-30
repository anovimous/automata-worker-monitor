package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

const token = "0123456789abcdef0123456789abcdef"

var t0 = time.Date(2026, 10, 29, 10, 42, 14, 0, time.UTC)

type stubStats map[string]controllerapi.Resources

func (s stubStats) Get(id string) (controllerapi.Resources, bool) { r, ok := s[id]; return r, ok }

type stubScrapes map[string]controllerapi.Telemetry

func (s stubScrapes) Get(id string) (controllerapi.Telemetry, bool) { t, ok := s[id]; return t, ok }

type stubVolumes struct {
	byWorker map[string]*controllerapi.Volumes
	total    *int64
}

func (s stubVolumes) Get(id string) *controllerapi.Volumes { return s.byWorker[id] }
func (s stubVolumes) Total() *int64                        { return s.total }

type stubHost struct{}

func (stubHost) Latest() controllerapi.HostMetrics {
	return controllerapi.HostMetrics{CPUs: 8, Disks: []controllerapi.Disk{}, Networks: []controllerapi.NetworkInterface{}}
}

type fixedPorts struct{}

func (fixedPorts) Allocate(map[int]bool) (int, error) { return 19100, nil }

type env struct {
	t       *testing.T
	engine  *docker.Fake
	disc    *discovery.Discovery
	stats   stubStats
	scrapes stubScrapes
	volumes stubVolumes
	h       http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	envDir := t.TempDir()
	os.WriteFile(filepath.Join(envDir, "default.env"), []byte("RABBITMQ_URL=amqp://automata:secret@rabbitmq:5672/\n"), 0o600)
	os.WriteFile(filepath.Join(envDir, "staging.env"), []byte("X=1\n"), 0o600)
	os.WriteFile(filepath.Join(envDir, "notes.txt"), []byte("x"), 0o600)

	f := docker.NewFake()
	f.AddImage("automata-worker:0.4.0")
	f.AddNetwork("automata_backend")
	disc := discovery.New(f)
	total := int64(1054871552)
	e := &env{t: t, engine: f, disc: disc, stats: stubStats{}, scrapes: stubScrapes{},
		volumes: stubVolumes{byWorker: map[string]*controllerapi.Volumes{}, total: &total}}
	prov := provision.New(f, config.Config{EnvDir: envDir, AllowedImages: []string{"automata-worker:*"}}, fixedPorts{}, disc, func() time.Time { return t0 })
	e.h = NewHandler(Deps{
		HostName: "host1", Version: "0.1.0", Token: token, EnvDir: envDir, DockerVersion: "29.6.1", DockerAPIVersion: "1.55",
		Engine: f, Workers: disc, Stats: e.stats, Scrapes: e.scrapes, Volumes: e.volumes, Host: stubHost{}, Provisioner: prov,
		Now: func() time.Time { return t0 },
	})
	return e
}

func (e *env) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return v
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeBody[controllerapi.ErrorResponse](t, rec).Error.Code
}

// create creates the worker of the contract sample and returns its container id.
func (e *env) create() string {
	e.t.Helper()
	body := string(contracttest.Read(e.t, "worker-machine-controller-create-worker.sample.json"))
	rec := e.do("POST", "/v1/workers", body)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST /v1/workers = %d %s", rec.Code, rec.Body)
	}
	return decodeBody[controllerapi.CreateWorkerResponse](e.t, rec).ContainerID
}

func TestEveryRouteNeedsTheToken(t *testing.T) {
	e := newEnv(t)
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + token + "x", token, "Basic " + token} {
		req := httptest.NewRequest("GET", "/v1/snapshot", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != controllerapi.CodeUnauthorized {
			t.Errorf("Authorization %q: %d %s, want 401 UNAUTHORIZED", auth, rec.Code, rec.Body)
		}
	}
	if rec := e.do("GET", "/v1/nothing", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown route = %d, want 404", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	h := decodeBody[controllerapi.HealthResponse](t, newEnv(t).do("GET", "/v1/health", ""))
	if h.HostName != "host1" || h.WorkerMachineControllerVersion != "0.1.0" || h.Docker.Version != "29.6.1" || h.Docker.APIVersion != "1.55" || !h.Now.Equal(t0) {
		t.Errorf("health = %+v", h)
	}
}

func TestCreateThenSnapshot(t *testing.T) {
	e := newEnv(t)
	id := e.create()
	e.stats[id] = controllerapi.Resources{CPUCores: 1.73}
	fetched := t0
	e.scrapes[id] = controllerapi.Telemetry{Configured: true, URL: "http://172.20.0.2:9464", OK: true, FetchedAt: &fetched,
		Status: &workerapi.Status{SchemaVersion: 1, WorkerID: "w-host1-k3f9qa"}}
	e.volumes.byWorker["w-host1-k3f9qa"] = &controllerapi.Volumes{SampledAt: t0, Items: []controllerapi.Volume{{Name: "automata-wl-w-host1-k3f9qa"}}}

	rec := e.do("GET", "/v1/snapshot", "")
	snap := decodeBody[controllerapi.Snapshot](t, rec)
	if snap.HostName != "host1" || snap.Host.CPUs != 8 || snap.Host.VolumesBytes == nil || *snap.Host.VolumesBytes != 1054871552 {
		t.Errorf("snapshot host = %+v", snap.Host)
	}
	if len(snap.Workers) != 1 {
		t.Fatalf("workers = %+v", snap.Workers)
	}
	o := snap.Workers[0]
	if o.WorkerID != "w-host1-k3f9qa" || !o.Managed || o.Template != "default" || o.Container.State != "running" {
		t.Errorf("observation = %+v", o)
	}
	if l := o.Container.Limits; l.CPUs == nil || *l.CPUs != 2 || *l.MemoryBytes != 4294967296 || *l.Pids != 512 {
		t.Errorf("limits = %+v", l)
	}
	if o.Resources == nil || o.Resources.CPUCores != 1.73 || !o.Telemetry.OK || o.Telemetry.Status.WorkerID != "w-host1-k3f9qa" {
		t.Errorf("resources %+v telemetry %+v", o.Resources, o.Telemetry)
	}
	if o.Volumes == nil || len(o.Volumes.Items) != 1 || len(o.Flags) != 0 {
		t.Errorf("volumes %+v flags %v", o.Volumes, o.Flags)
	}

	// the snapshot decodes strictly into the contract types
	var strict controllerapi.Snapshot
	contracttest.DecodeStrict(t, rec.Body.Bytes(), &strict)
}

func TestTheSnapshotOfAStoppedWorker(t *testing.T) {
	e := newEnv(t)
	id := e.create()
	e.stats[id] = controllerapi.Resources{CPUCores: 1}
	e.scrapes[id] = controllerapi.Telemetry{Configured: true, OK: true}
	e.engine.Stop(context.Background(), id, time.Second)
	e.disc.Update(context.Background(), id)

	o := decodeBody[controllerapi.Snapshot](t, e.do("GET", "/v1/snapshot", "")).Workers[0]
	if o.Resources != nil || o.Telemetry.OK || o.Telemetry.Status != nil || !o.Telemetry.Configured {
		t.Errorf("stopped worker: resources %+v telemetry %+v, want no resources and no status", o.Resources, o.Telemetry)
	}
}

func TestAnEmptySnapshot(t *testing.T) {
	rec := newEnv(t).do("GET", "/v1/snapshot", "")
	if !strings.Contains(rec.Body.String(), `"workers":[]`) {
		t.Errorf("snapshot = %s, want workers []", rec.Body)
	}
}

func TestCreateErrors(t *testing.T) {
	e := newEnv(t)
	sample := string(contracttest.Read(t, "worker-machine-controller-create-worker.sample.json"))
	tests := []struct {
		name, body string
		status     int
		code       string
	}{
		{"not json", "{", 400, controllerapi.CodeInvalid},
		{"invalid", strings.Replace(sample, `"stopTimeoutSeconds": 60`, `"stopTimeoutSeconds": 5`, 1), 400, controllerapi.CodeInvalid},
		{"image", strings.Replace(sample, `"automata-worker:0.4.0"`, `"ubuntu:24.04"`, 1), 400, controllerapi.CodeImageNotAllowed},
		{"too big", `{"workerId":"` + strings.Repeat("a", maxBody) + `"}`, 400, controllerapi.CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do("POST", "/v1/workers", tt.body)
			if rec.Code != tt.status || errorCode(t, rec) != tt.code {
				t.Errorf("POST /v1/workers = %d %s, want %d %s", rec.Code, rec.Body, tt.status, tt.code)
			}
		})
	}

	id := e.create()
	rec := e.do("POST", "/v1/workers", sample)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), id) {
		t.Errorf("a second create = %d %s, want 409 naming the container", rec.Code, rec.Body)
	}
}

func TestGetWorkerShowsEnvKeysOnly(t *testing.T) {
	e := newEnv(t)
	e.create()

	rec := e.do("GET", "/v1/workers/w-host1-k3f9qa", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("GET worker = %d %s, want 200 without the env values", rec.Code, rec.Body)
	}
	d := decodeBody[controllerapi.WorkerDetail](t, rec)
	if d.WorkerID != "w-host1-k3f9qa" || len(d.Details.Mounts) != 3 || d.Details.RestartPolicy.Name != "on-failure" || *d.Details.StopTimeout != 60 {
		t.Errorf("detail = %+v", d)
	}
	found := false
	for _, k := range d.Details.EnvKeys {
		found = found || k == "RABBITMQ_URL"
	}
	if !found {
		t.Errorf("env keys = %v, want RABBITMQ_URL among them", d.Details.EnvKeys)
	}
	if rec := e.do("GET", "/v1/workers/w-host1-nope00", ""); rec.Code != 404 {
		t.Errorf("unknown worker = %d, want 404", rec.Code)
	}
}

func TestSignalAcceptsDrainAndUndrainOnly(t *testing.T) {
	e := newEnv(t)
	e.create()
	for sig, want := range map[string]int{"SIGUSR1": 200, "SIGUSR2": 200, "SIGKILL": 400, "SIGTERM": 400, "": 400} {
		rec := e.do("POST", "/v1/workers/w-host1-k3f9qa/signal", `{"signal":"`+sig+`"}`)
		if rec.Code != want {
			t.Errorf("signal %q = %d %s, want %d", sig, rec.Code, rec.Body, want)
		}
	}
	if rec := e.do("POST", "/v1/workers/w-host1-k3f9qa/signal", `{"signal":"SIGUSR1"}`); decodeBody[controllerapi.WorkerObservation](t, rec).Container.State != "running" {
		t.Error("a drain signal stopped the container")
	}
}

func TestStopKillRestartStart(t *testing.T) {
	e := newEnv(t)
	e.create()
	state := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return decodeBody[controllerapi.WorkerObservation](t, rec).Container.State
	}

	if s := state(e.do("POST", "/v1/workers/w-host1-k3f9qa/stop", `{"timeoutSeconds": 60}`)); s != "exited" {
		t.Errorf("after stop: %s", s)
	}
	if s := state(e.do("POST", "/v1/workers/w-host1-k3f9qa/start", "")); s != "running" {
		t.Errorf("after start: %s", s)
	}
	if s := state(e.do("POST", "/v1/workers/w-host1-k3f9qa/restart", "")); s != "running" {
		t.Errorf("after restart: %s", s)
	}
	if s := state(e.do("POST", "/v1/workers/w-host1-k3f9qa/kill", "")); s != "exited" {
		t.Errorf("after kill: %s", s)
	}
	if rec := e.do("POST", "/v1/workers/w-host1-k3f9qa/kill", ""); rec.Code != http.StatusConflict {
		t.Errorf("kill of a stopped worker = %d, want 409", rec.Code)
	}
	for _, body := range []string{`{"timeoutSeconds": 601}`, `{"timeoutSeconds": -1}`, `nope`} {
		if rec := e.do("POST", "/v1/workers/w-host1-k3f9qa/stop", body); rec.Code != 400 {
			t.Errorf("stop %s = %d, want 400", body, rec.Code)
		}
	}
	if rec := e.do("POST", "/v1/workers/w-host1-nope00/stop", ""); rec.Code != 404 {
		t.Errorf("stop of an unknown worker = %d, want 404", rec.Code)
	}
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	id := e.create()

	if rec := e.do("DELETE", "/v1/workers/w-host1-k3f9qa?removeVolumes=true", ""); rec.Code != http.StatusConflict {
		t.Fatalf("remove of a running worker = %d, want 409", rec.Code)
	}
	e.engine.Stop(context.Background(), id, time.Second)
	// the cache volume was replaced by one that is not the worker's: it must be left alone
	e.engine.VolumeRemove(context.Background(), "automata-wl-w-host1-k3f9qa")
	e.engine.CreateVolume("automata-wl-w-host1-k3f9qa", map[string]string{"owner": "someone"})

	rec := e.do("DELETE", "/v1/workers/w-host1-k3f9qa?removeVolumes=true", "")
	if rec.Code != 200 {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	resp := decodeBody[controllerapi.RemoveResponse](t, rec)
	if len(resp.VolumesNotRemoved) != 1 || resp.VolumesNotRemoved[0].Name != "automata-wl-w-host1-k3f9qa" {
		t.Errorf("volumesNotRemoved = %+v, want the foreign cache volume", resp.VolumesNotRemoved)
	}
	if vols := e.engine.Volumes(); len(vols) != 1 || vols[0] != "automata-wl-w-host1-k3f9qa" {
		t.Errorf("volumes left = %v, want only the foreign one", vols)
	}
	if rec := e.do("GET", "/v1/workers/w-host1-k3f9qa", ""); rec.Code != 404 {
		t.Errorf("the removed worker is still there: %d", rec.Code)
	}
}

func TestRemoveKeepsTheVolumesUnlessAsked(t *testing.T) {
	e := newEnv(t)
	id := e.create()
	e.engine.Stop(context.Background(), id, time.Second)

	rec := e.do("DELETE", "/v1/workers/w-host1-k3f9qa", "")
	if rec.Code != 200 || len(decodeBody[controllerapi.RemoveResponse](t, rec).VolumesNotRemoved) != 0 {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	if len(e.engine.Volumes()) != 2 {
		t.Errorf("volumes = %v, want both kept", e.engine.Volumes())
	}
}

func TestLogs(t *testing.T) {
	e := newEnv(t)
	id := e.create()
	e.engine.SetLogs(id, "2026-10-29T10:42:13Z job started\n", "2026-10-29T10:42:14Z dial amqp://u:secret@h failed\n")

	rec := e.do("GET", "/v1/workers/w-host1-k3f9qa/logs?tail=10", "")
	lines := decodeBody[[]controllerapi.LogLine](t, rec)
	if len(lines) != 2 || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("logs = %s, want 2 redacted lines", rec.Body)
	}

	rec = e.do("GET", "/v1/workers/w-host1-k3f9qa/logs?follow=true", "")
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("follow content type = %q", rec.Header().Get("Content-Type"))
	}
	events := 0
	sc := bufio.NewScanner(bytes.NewReader(rec.Body.Bytes()))
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var l controllerapi.LogLine
			if err := json.Unmarshal([]byte(data), &l); err != nil {
				t.Errorf("event %q: %v", data, err)
			}
			events++
		}
	}
	if events != 2 {
		t.Errorf("events = %d, want 2", events)
	}

	for _, q := range []string{"tail=x", "since=yesterday"} {
		if rec := e.do("GET", "/v1/workers/w-host1-k3f9qa/logs?"+q, ""); rec.Code != 400 {
			t.Errorf("logs?%s = %d, want 400", q, rec.Code)
		}
	}
}

func TestEnvFilesListsNamesOnly(t *testing.T) {
	rec := newEnv(t).do("GET", "/v1/env-files", "")
	names := decodeBody[[]string](t, rec)
	if len(names) != 2 || names[0] != "default" || names[1] != "staging" || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("env files = %s", rec.Body)
	}
}

func TestImages(t *testing.T) {
	imgs := decodeBody[[]controllerapi.Image](t, newEnv(t).do("GET", "/v1/images?reference=automata-worker", ""))
	if len(imgs) != 1 || imgs[0].Tags[0] != "automata-worker:0.4.0" {
		t.Errorf("images = %+v", imgs)
	}
}

func TestDockerErrorsAreMapped(t *testing.T) {
	e := newEnv(t)
	e.create()
	e.engine.SetErr("Inspect", io.ErrUnexpectedEOF)
	if rec := e.do("GET", "/v1/workers/w-host1-k3f9qa", ""); rec.Code != http.StatusBadGateway || errorCode(t, rec) != controllerapi.CodeDocker {
		t.Errorf("a Docker failure = %d %s, want 502 DOCKER", rec.Code, rec.Body)
	}
}

func TestErrorMessagesAreRedacted(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, 502, controllerapi.CodeDocker, "pull failed: Get https://user:hunter2@registry/v2/")
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("error = %s, holds the password", rec.Body)
	}
}
