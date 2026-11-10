package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/controllerclient"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/fleet"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/sse"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	mcapi "github.com/anovimous/automata-worker-monitor/internal/machinecontroller/api"
	mcconfig "github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

const token = "0123456789abcdef0123456789abcdef"

type noStats struct{}

func (noStats) Get(string) (controllerapi.Resources, bool) {
	return controllerapi.Resources{CPUCores: 0.4, Pids: 5}, true
}

// okScrapes says every worker answers its status, at the clock of the test.
type okScrapes struct{ now func() time.Time }

func (s okScrapes) Get(id string) (controllerapi.Telemetry, bool) {
	now := s.now()
	return controllerapi.Telemetry{Configured: true, OK: true, FetchedAt: &now, LatencyMs: new(int64),
		Status: &workerapi.Status{SchemaVersion: 1, Version: "0.4.0", Now: now, LastProgressAt: now, Phase: workerapi.PhaseIdle,
			PhaseBudgetSeconds: 30, Counters: workerapi.Counters{JobsByOutcome: map[string]int64{}}}}, true
}

type noVolumes struct{}

func (noVolumes) Get(string) *controllerapi.Volumes { return nil }
func (noVolumes) Total() *int64                     { return nil }

type someHost struct{}

func (someHost) Latest() controllerapi.HostMetrics {
	return controllerapi.HostMetrics{CPUs: 8, MemoryTotalBytes: 32 << 30, MemoryAvailableBytes: 20 << 30,
		Disks: []controllerapi.Disk{}, Networks: []controllerapi.NetworkInterface{}}
}

type onePort struct{}

func (onePort) Allocate(map[int]bool) (int, error) { return 19100, nil }

type env struct {
	t      *testing.T
	h      http.Handler
	fleet  *fleet.Fleet
	store  *store.Store
	docker *docker.Fake
	mu     sync.Mutex
	clock  time.Time
}

func (e *env) now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clock: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}

	// the real worker machine controller, on a fake Docker
	envDir := t.TempDir()
	os.WriteFile(filepath.Join(envDir, "default.env"), []byte("RABBITMQ_URL=amqp://u:secret@h/\n"), 0o600)
	e.docker = docker.NewFake()
	e.docker.AddImage(store.DefaultTemplate().Image) // the image the seeded default template names
	e.docker.AddNetwork("automata_backend")
	disc := discovery.New(e.docker)
	prov := provision.New(e.docker, mcconfig.Config{EnvDir: envDir, AllowedImages: []string{"automata-worker:*"}}, onePort{}, disc, e.now)
	controller := httptest.NewServer(mcapi.NewHandler(mcapi.Deps{HostName: "host1", Version: "0.1.0", Token: token,
		EnvDir: envDir, Engine: e.docker, Workers: disc, Stats: noStats{}, Scrapes: okScrapes{e.now}, Volumes: noVolumes{},
		Host: someHost{}, Provisioner: prov, Now: e.now}))
	t.Cleanup(controller.Close)

	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Name: "host1", WorkerMachineControllerURL: controller.URL, ControlToken: token, MaxWorkers: 5}}
	st, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SeedDefault(context.Background(), e.clock)
	client, _ := controllerclient.New(cfg.Hosts[0])
	broker := sse.NewBroker()
	e.store = st
	e.fleet = fleet.New(cfg, st, map[string]fleet.Client{"host1": client}, broker, e.now)
	if err := e.fleet.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.h = NewHandler(Deps{Fleet: e.fleet, Store: st, Broker: broker, PollIntervalSeconds: 5, HistoryRetentionDays: 14, Now: e.now})
	return e
}

func (e *env) poll() {
	e.mu.Lock()
	e.clock = e.clock.Add(5 * time.Second)
	e.mu.Unlock()
	e.fleet.PollHost(context.Background(), "host1")
}

func (e *env) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
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

func codeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeBody[errorBody](t, rec).Error.Code
}

// createOne creates a worker of the default template and polls until it is active.
func (e *env) createOne() string {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/workers", `{"host":"host1","templateId":1,"count":1}`)
	if rec.Code != 201 {
		e.t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	res := decodeBody[fleet.CreateResult](e.t, rec)
	if !res.Workers[0].OK {
		e.t.Fatalf("create = %+v", res)
	}
	e.poll()
	return res.Workers[0].ID
}

func TestOriginCheck(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("POST", "/api/v1/hosts/host1/cordon", `{}`, "Origin", "https://evil.example"); rec.Code != 403 || codeOf(t, rec) != "FORBIDDEN_ORIGIN" {
		t.Errorf("a foreign origin = %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/v1/hosts/host1/cordon", `{}`, "Origin", "http://example.com"); rec.Code != 200 {
		t.Errorf("the own origin (httptest's Host example.com) = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/v1/hosts/host1/uncordon", ``); rec.Code != 200 {
		t.Errorf("no origin = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/overview", "", "Origin", "null"); rec.Code != 403 {
		t.Errorf("Origin null = %d, want 403", rec.Code)
	}
}

func TestHealthzAndUI(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/healthz", ""); rec.Code != 503 {
		t.Errorf("healthz before a poll = %d, want 503", rec.Code)
	}
	e.poll()
	if rec := e.do("GET", "/healthz", ""); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body)
	}
	for _, path := range []string{"/", "/workers/w-1", "/templates"} {
		if rec := e.do("GET", path, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Automata workers") {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
	}
	if rec := e.do("GET", "/api/v1/nothing", ""); rec.Code != 404 || codeOf(t, rec) != "NOT_FOUND" {
		t.Errorf("unknown API route = %d %s", rec.Code, rec.Body)
	}
}

func TestTemplates(t *testing.T) {
	e := newEnv(t)
	list := decodeBody[[]store.Template](t, e.do("GET", "/api/v1/templates", ""))
	if len(list) != 1 || list[0].Name != "default" {
		t.Fatalf("templates = %+v", list)
	}

	body := `{"name":"small","image":"automata-worker:0.4.0","pullPolicy":"never","envFile":"default","network":"host",
		"cpus":1,"memoryBytes":1073741824,"wordlistCacheMaxBytes":536870912,"drainTimeoutSeconds":600,"removeAfterDrain":true}`
	rec := e.do("POST", "/api/v1/templates", body)
	if rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	small := decodeBody[store.Template](t, rec)
	if small.ID == 0 || small.StopTimeoutSeconds != 60 || small.PidsLimit != 512 || small.TmpfsSocketsBytes != 16<<20 || small.RestartMaxRetries != 5 {
		t.Errorf("created = %+v, want the defaults filled", small)
	}
	if rec := e.do("POST", "/api/v1/templates", body); rec.Code != 409 {
		t.Errorf("same name = %d, want 409", rec.Code)
	}
	bad := map[string]string{
		"latest image":  strings.Replace(body, "0.4.0", "latest", 1),
		"unknown field": strings.Replace(body, `"name"`, `"nmae":"x","name"`, 1),
		"not json":      "{",
	}
	for name, b := range bad {
		if rec := e.do("POST", "/api/v1/templates", b); rec.Code != 400 {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}

	path := "/api/v1/templates/" + itoa(small.ID)
	rec = e.do("PUT", path, strings.Replace(body, `"cpus":1`, `"cpus":1.5`, 1))
	if rec.Code != 200 || decodeBody[store.Template](t, rec).CPUs != 1.5 {
		t.Errorf("update = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", path, ""); rec.Code != 200 {
		t.Errorf("get = %d", rec.Code)
	}
	if rec := e.do("DELETE", path, ""); rec.Code != 204 {
		t.Errorf("delete = %d", rec.Code)
	}
	for _, p := range []string{path, "/api/v1/templates/x"} {
		if rec := e.do("GET", p, ""); rec.Code != 404 && rec.Code != 400 {
			t.Errorf("GET %s after delete = %d", p, rec.Code)
		}
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestWorkersFromCreateToRemove(t *testing.T) {
	e := newEnv(t)
	id := e.createOne()

	list := decodeBody[struct {
		Items      []fleet.WorkerView `json:"items"`
		NextCursor string             `json:"nextCursor"`
	}](t, e.do("GET", "/api/v1/workers?health=HEALTHY&host=host1", ""))
	if len(list.Items) != 1 || list.Items[0].ID != id || list.Items[0].Lifecycle != "ACTIVE" || list.NextCursor != "" {
		t.Fatalf("workers = %+v", list)
	}
	if empty := decodeBody[struct {
		Items []fleet.WorkerView `json:"items"`
	}](t, e.do("GET", "/api/v1/workers?lifecycle=STOPPED", "")); len(empty.Items) != 0 {
		t.Errorf("filter STOPPED = %+v", empty.Items)
	}

	rec := e.do("GET", "/api/v1/workers/"+id, "")
	detail := decodeBody[struct {
		Worker           fleet.WorkerView            `json:"worker"`
		TemplateSnapshot store.Template              `json:"templateSnapshot"`
		Container        *controllerapi.WorkerDetail `json:"container"`
	}](t, rec)
	if detail.Worker.ID != id || detail.TemplateSnapshot.Name != "default" || detail.Container == nil || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("worker detail = %s", rec.Body)
	}

	if rec := e.do("POST", "/api/v1/workers/"+id+"/actions/start", ""); rec.Code != 409 || codeOf(t, rec) != "INVALID_TRANSITION" {
		t.Errorf("start of an active worker = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/api/v1/workers/"+id+"/actions/explode", ""); rec.Code != 404 {
		t.Errorf("unknown action = %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/workers/"+id+"/actions/stop", `{"timeoutSeconds":700}`); rec.Code != 400 {
		t.Errorf("stop with 700 s = %d", rec.Code)
	}
	rec = e.do("POST", "/api/v1/workers/"+id+"/actions/drain", "")
	if res := decodeBody[fleet.ActionResult](t, rec); rec.Code != 200 || res.Worker.Lifecycle != "DRAINING" {
		t.Errorf("drain = %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/api/v1/workers/"+id+"/actions/stop", `{"timeoutSeconds":45}`)
	if res := decodeBody[fleet.ActionResult](t, rec); rec.Code != 200 || res.Worker.Lifecycle != "STOPPING" {
		t.Errorf("stop = %d %s", rec.Code, rec.Body)
	}
	e.poll()
	rec = e.do("POST", "/api/v1/workers/"+id+"/actions/remove", `{"removeVolumes":true}`)
	if res := decodeBody[fleet.ActionResult](t, rec); rec.Code != 200 || res.Worker.Lifecycle != "REMOVED" || len(res.Warnings) != 0 {
		t.Errorf("remove = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/workers/"+id, ""); rec.Code != 404 {
		t.Errorf("the removed worker = %d", rec.Code)
	}

	evs := decodeBody[[]store.Event](t, e.do("GET", "/api/v1/workers/"+id+"/events?limit=3", ""))
	if len(evs) != 3 || evs[0].Kind != "LIFECYCLE_CHANGED" || evs[0].To != "REMOVED" {
		t.Errorf("events = %+v", evs)
	}
	older := decodeBody[[]store.Event](t, e.do("GET", "/api/v1/workers/"+id+"/events?before="+itoa(evs[2].ID), ""))
	if len(older) == 0 || older[0].ID >= evs[2].ID {
		t.Errorf("older events = %+v", older)
	}
	for _, q := range []string{"?limit=0", "?limit=501", "?before=x"} {
		if rec := e.do("GET", "/api/v1/workers/"+id+"/events"+q, ""); rec.Code != 400 {
			t.Errorf("events%s = %d", q, rec.Code)
		}
	}
}

func TestCreateRefusals(t *testing.T) {
	e := newEnv(t)
	tests := map[string]struct {
		body   string
		status int
		code   string
	}{
		"too many":    {`{"host":"host1","templateId":1,"count":6}`, 409, "MAX_WORKERS"},
		"no template": {`{"host":"host1","templateId":9,"count":1}`, 404, "NOT_FOUND"},
		"no host":     {`{"host":"host9","templateId":1,"count":1}`, 404, "NOT_FOUND"},
		"bad count":   {`{"host":"host1","templateId":1,"count":21}`, 400, "INVALID"},
		"unknown key": {`{"host":"host1","templateId":1,"namePrefix":"x"}`, 400, "INVALID"},
	}
	for name, tt := range tests {
		rec := e.do("POST", "/api/v1/workers", tt.body)
		if rec.Code != tt.status || codeOf(t, rec) != tt.code {
			t.Errorf("%s: %d %s, want %d %s", name, rec.Code, rec.Body, tt.status, tt.code)
		}
	}
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/workers", `{"host":"host1","templateId":1,"count":3}`)
	type page struct {
		Items      []fleet.WorkerView `json:"items"`
		NextCursor string             `json:"nextCursor"`
	}
	first := decodeBody[page](t, e.do("GET", "/api/v1/workers?limit=2", ""))
	if len(first.Items) != 2 || first.NextCursor != first.Items[1].ID {
		t.Fatalf("first page = %+v", first)
	}
	second := decodeBody[page](t, e.do("GET", "/api/v1/workers?limit=2&cursor="+first.NextCursor, ""))
	if len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].ID <= first.NextCursor {
		t.Errorf("second page = %+v", second)
	}
}

func TestHostsOverviewAndProxies(t *testing.T) {
	e := newEnv(t)
	e.createOne()

	hosts := decodeBody[[]fleet.HostView](t, e.do("GET", "/api/v1/hosts", ""))
	if len(hosts) != 1 || !hosts[0].Reachable || hosts[0].Workers.Total != 1 || strings.Contains(hosts[0].URL, token) {
		t.Errorf("hosts = %+v", hosts)
	}
	if rec := e.do("GET", "/api/v1/hosts/host9", ""); rec.Code != 404 {
		t.Errorf("unknown host = %d", rec.Code)
	}
	rec := e.do("POST", "/api/v1/hosts/host1/cordon", `{"reason":"disk full"}`)
	if h := decodeBody[fleet.HostView](t, rec); !h.Cordoned || h.CordonedReason != "disk full" {
		t.Errorf("cordon = %s", rec.Body)
	}
	if rec := e.do("POST", "/api/v1/workers", `{"host":"host1","templateId":1}`); rec.Code != 409 || codeOf(t, rec) != "HOST_CORDONED" {
		t.Errorf("create on a cordoned host = %d %s", rec.Code, rec.Body)
	}
	e.do("POST", "/api/v1/hosts/host1/uncordon", "")

	ov := decodeBody[fleet.Overview](t, e.do("GET", "/api/v1/overview", ""))
	if ov.Workers != 1 || ov.ByHealth["HEALTHY"] != 1 {
		t.Errorf("overview = %+v", ov)
	}
	if names := decodeBody[[]string](t, e.do("GET", "/api/v1/hosts/host1/env-files", "")); len(names) != 1 || names[0] != "default" {
		t.Errorf("env files = %v", names)
	}
	if imgs := decodeBody[[]controllerapi.Image](t, e.do("GET", "/api/v1/hosts/host1/images?reference=automata-worker", "")); len(imgs) != 1 {
		t.Errorf("images = %v", imgs)
	}
	cfg := decodeBody[map[string]any](t, e.do("GET", "/api/v1/config/ui", ""))
	if cfg["pollIntervalSeconds"] != float64(5) || cfg["historyRetentionDays"] != float64(14) || cfg["databaseBytes"].(float64) <= 0 {
		t.Errorf("config = %v", cfg)
	}
	if _, ok := cfg["jobUrlTemplate"]; ok {
		t.Error("jobUrlTemplate is there, A4 is rejected")
	}
}

func TestTimeseriesRoutes(t *testing.T) {
	e := newEnv(t)
	id := e.createOne()
	e.poll()
	ts := decodeBody[fleet.Timeseries](t, e.do("GET", "/api/v1/workers/"+id+"/timeseries?series=cpu&range=15m", ""))
	if ts.Source != "buffer" || len(ts.Series) != 2 || len(ts.Series[0].Points) == 0 {
		t.Errorf("worker timeseries = %+v", ts)
	}
	if all := decodeBody[fleet.Timeseries](t, e.do("GET", "/api/v1/hosts/host1/timeseries", "")); len(all.Series) < 5 {
		t.Errorf("host timeseries = %+v", all)
	}
	for _, q := range []string{"?range=30d", "?series=temperature"} {
		if rec := e.do("GET", "/api/v1/workers/"+id+"/timeseries"+q, ""); rec.Code != 400 {
			t.Errorf("timeseries%s = %d", q, rec.Code)
		}
	}
	if long := decodeBody[fleet.Timeseries](t, e.do("GET", "/api/v1/workers/"+id+"/timeseries?series=health&range=24h", "")); long.Source != "samples" || long.BucketSeconds != 300 {
		t.Errorf("24h timeseries = %+v", long)
	}
}

func TestHistoryRoutes(t *testing.T) {
	e := newEnv(t)
	id := e.createOne()
	for range 13 {
		e.poll()
	}
	a := decodeBody[fleet.Availability](t, e.do("GET", "/api/v1/workers/"+id+"/availability?range=24h", ""))
	if a.Polls == 0 || a.Availability == nil {
		t.Errorf("availability = %+v", a)
	}
	all := decodeBody[struct {
		Range   string                        `json:"range"`
		Workers map[string]fleet.Availability `json:"workers"`
	}](t, e.do("GET", "/api/v1/availability", ""))
	if all.Range != "24h" || all.Workers[id].Polls != a.Polls {
		t.Errorf("fleet availability = %+v", all)
	}
	ts := decodeBody[fleet.Timeseries](t, e.do("GET", "/api/v1/fleet/timeseries?series=capacity&range=6h", ""))
	if ts.Source != "samples" || len(ts.Series) != 3 || ts.Series[0].Name != "active" {
		t.Errorf("fleet timeseries = %+v", ts)
	}
	for _, path := range []string{"/api/v1/workers/" + id + "/availability?range=1h", "/api/v1/availability?range=14d",
		"/api/v1/fleet/timeseries?series=cpu"} {
		if rec := e.do("GET", path, ""); rec.Code != 400 {
			t.Errorf("%s = %d, want 400", path, rec.Code)
		}
	}
	if rec := e.do("GET", "/api/v1/workers/w-nope/availability", ""); rec.Code != 404 {
		t.Errorf("availability of an unknown worker = %d", rec.Code)
	}
}

func TestLogs(t *testing.T) {
	e := newEnv(t)
	id := e.createOne()
	v, _ := e.fleet.Worker(id)
	e.docker.SetLogs(v.ContainerID, "2026-10-29T10:42:13Z job started\n2026-10-29T10:42:14Z TOKEN=abc\n", "")

	rec := e.do("GET", "/api/v1/workers/"+id+"/logs?tail=10", "")
	lines := decodeBody[[]controllerapi.LogLine](t, rec)
	if rec.Code != 200 || len(lines) != 2 || lines[1].Line != "TOKEN=***" {
		t.Errorf("logs = %d %s", rec.Code, rec.Body)
	}

	rec = e.do("GET", "/api/v1/workers/"+id+"/logs?follow=true", "")
	if rec.Header().Get("Content-Type") != "text/event-stream" || strings.Contains(rec.Body.String(), "abc") {
		t.Errorf("follow = %s %s", rec.Header().Get("Content-Type"), rec.Body)
	}
	data := 0
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: {") {
			data++
		}
	}
	if data != 2 {
		t.Errorf("follow events = %d, want 2", data)
	}
	if rec := e.do("GET", "/api/v1/workers/w-nope/logs", ""); rec.Code != 404 {
		t.Errorf("logs of an unknown worker = %d", rec.Code)
	}
}

func TestEventsStreamStartsWithASnapshot(t *testing.T) {
	e := newEnv(t)
	e.createOne()
	ts := httptest.NewServer(e.h)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var snap struct {
				Workers []fleet.WorkerView `json:"workers"`
				Hosts   []fleet.HostView   `json:"hosts"`
			}
			if err := json.Unmarshal([]byte(data), &snap); err != nil || len(snap.Workers) != 1 || len(snap.Hosts) != 1 {
				t.Errorf("snapshot = %s, %v", data, err)
			}
			return
		}
	}
	t.Fatal("no snapshot event")
}
