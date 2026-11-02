package fleet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/controllerclient"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/sse"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// fakeClient is a worker machine controller the test scripts: it keeps observations by worker id and records the
// calls.
type fakeClient struct {
	mu      sync.Mutex
	workers map[string]*controllerapi.WorkerObservation
	host    controllerapi.HostMetrics
	calls   []string
	errs    map[string]error
	removed controllerapi.RemoveResponse
	// blockStop makes Stop wait until it is closed
	blockStop chan struct{}
	stopped   chan struct{}
}

func newFakeClient() *fakeClient {
	return &fakeClient{workers: map[string]*controllerapi.WorkerObservation{}, errs: map[string]error{},
		host: controllerapi.HostMetrics{CPUs: 8, MemoryTotalBytes: 32 << 30, MemoryAvailableBytes: 16 << 30,
			Disks: []controllerapi.Disk{{Path: "/host/var/lib/docker", TotalBytes: 100, FreeBytes: 40}}}}
}

func (c *fakeClient) record(call string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	return c.errs[call]
}

func (c *fakeClient) setErr(call string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs[call] = err
}

func (c *fakeClient) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// set changes the observation of a worker (creating it when missing).
func (c *fakeClient) set(id string, fn func(*controllerapi.WorkerObservation)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.workers[id]
	if !ok {
		o = observation(id)
		c.workers[id] = o
	}
	fn(o)
}

func (c *fakeClient) drop(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.workers, id)
}

func (c *fakeClient) Snapshot(ctx context.Context) (controllerapi.Snapshot, error) {
	if err := c.record("snapshot"); err != nil {
		return controllerapi.Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := controllerapi.Snapshot{HostName: "host1", WorkerMachineControllerVersion: "0.1.0", Host: c.host}
	ids := make([]string, 0, len(c.workers))
	for id := range c.workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		snap.Workers = append(snap.Workers, deepCopy(c.workers[id]))
	}
	return snap, nil
}

func deepCopy(o *controllerapi.WorkerObservation) controllerapi.WorkerObservation {
	cp := *o
	if o.Telemetry.Status != nil {
		s := *o.Telemetry.Status
		if s.Job != nil {
			j := *s.Job
			s.Job = &j
		}
		cp.Telemetry.Status = &s
	}
	if o.Resources != nil {
		r := *o.Resources
		cp.Resources = &r
	}
	return cp
}

func (c *fakeClient) Worker(ctx context.Context, id string) (controllerapi.WorkerDetail, error) {
	return controllerapi.WorkerDetail{}, c.record("worker " + id)
}

func (c *fakeClient) Create(ctx context.Context, req controllerapi.CreateWorkerRequest) (controllerapi.CreateWorkerResponse, error) {
	if err := c.record("create " + req.WorkerID); err != nil {
		return controllerapi.CreateWorkerResponse{}, err
	}
	c.set(req.WorkerID, func(o *controllerapi.WorkerObservation) {
		o.Container.Limits.CPUs = &req.Template.CPUs
		o.Container.Limits.MemoryBytes = &req.Template.MemoryBytes
		o.Telemetry = controllerapi.Telemetry{Configured: true}
	})
	return controllerapi.CreateWorkerResponse{WorkerID: req.WorkerID, ContainerID: "c-" + req.WorkerID}, nil
}

// act applies an action to the observation and returns it.
func (c *fakeClient) act(call, id string, fn func(*controllerapi.WorkerObservation)) (controllerapi.WorkerObservation, error) {
	if err := c.record(call + " " + id); err != nil {
		return controllerapi.WorkerObservation{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.workers[id]
	if !ok {
		return controllerapi.WorkerObservation{}, &controllerclient.APIError{Status: 404, Code: "NOT_FOUND", Message: "no worker"}
	}
	fn(o)
	return deepCopy(o), nil
}

func exited(code int) func(*controllerapi.WorkerObservation) {
	return func(o *controllerapi.WorkerObservation) {
		o.Container.State, o.Container.ExitCode, o.Resources = "exited", code, nil
		o.Telemetry.OK, o.Telemetry.Status = false, nil
	}
}

func (c *fakeClient) Start(ctx context.Context, id string) (controllerapi.WorkerObservation, error) {
	return c.act("start", id, func(o *controllerapi.WorkerObservation) { o.Container.State = "running" })
}

func (c *fakeClient) Signal(ctx context.Context, id, signal string) (controllerapi.WorkerObservation, error) {
	return c.act("signal:"+signal, id, func(*controllerapi.WorkerObservation) {})
}

func (c *fakeClient) Stop(ctx context.Context, id string, timeout time.Duration) (controllerapi.WorkerObservation, error) {
	c.mu.Lock()
	block, stopped := c.blockStop, c.stopped
	c.mu.Unlock()
	if block != nil {
		// the worker exits 0 during the stop, before the call returns
		c.set(id, exited(0))
		close(stopped)
		<-block
	}
	return c.act("stop", id, exited(0))
}

func (c *fakeClient) Kill(ctx context.Context, id string) (controllerapi.WorkerObservation, error) {
	return c.act("kill", id, exited(137))
}

func (c *fakeClient) Restart(ctx context.Context, id string, timeout time.Duration) (controllerapi.WorkerObservation, error) {
	return c.act("restart", id, func(o *controllerapi.WorkerObservation) { o.Container.State = "running" })
}

func (c *fakeClient) Remove(ctx context.Context, id string, removeVolumes bool) (controllerapi.RemoveResponse, error) {
	call := "remove"
	if removeVolumes {
		call = "remove+volumes"
	}
	if err := c.record(call + " " + id); err != nil {
		return controllerapi.RemoveResponse{}, err
	}
	c.drop(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removed, nil
}

func (c *fakeClient) Logs(ctx context.Context, id string, opts controllerclient.LogOptions) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader([]byte("[]"))), "application/json", c.record("logs " + id)
}

func (c *fakeClient) EnvFiles(ctx context.Context) ([]string, error) {
	return []string{"default"}, c.record("env-files")
}

func (c *fakeClient) Images(ctx context.Context, reference string) ([]controllerapi.Image, error) {
	return []controllerapi.Image{}, c.record("images")
}

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

// observation is a healthy idle worker as the controller reports it at the test clock's time.
func observation(id string) *controllerapi.WorkerObservation {
	started := t0
	return &controllerapi.WorkerObservation{
		WorkerID: id, Managed: true, Template: "default",
		Container: controllerapi.Container{ID: "c-" + id, Name: "automata-worker-" + id, Image: "automata-worker:0.4.0",
			State: "running", StartedAt: &started, Labels: map[string]string{
				workerapi.LabelID: id, workerapi.LabelManagedBy: workerapi.ManagedByMonitor, workerapi.LabelTemplate: "default"}},
		Resources: &controllerapi.Resources{CPUCores: 0.5, MemoryWorkingSetBytes: 100 << 20, Pids: 7},
		Telemetry: controllerapi.Telemetry{Configured: true, OK: true},
		Flags:     []string{},
	}
}

// ready makes a worker answer a good status at now.
func ready(now time.Time) func(*controllerapi.WorkerObservation) {
	return func(o *controllerapi.WorkerObservation) {
		fetched := now
		o.Container.State = "running"
		o.Telemetry = controllerapi.Telemetry{Configured: true, OK: true, FetchedAt: &fetched, LatencyMs: new(int64),
			Status: &workerapi.Status{SchemaVersion: 1, WorkerID: o.WorkerID, Version: "0.4.0", Now: now, Phase: workerapi.PhaseIdle,
				LastProgressAt: now, PhaseBudgetSeconds: 30, Counters: workerapi.Counters{JobsByOutcome: map[string]int64{}}}}
		if o.Resources == nil {
			o.Resources = &controllerapi.Resources{CPUCores: 0.5, MemoryWorkingSetBytes: 100 << 20, Pids: 7}
		}
	}
}

type env struct {
	t       *testing.T
	f       *Fleet
	client  *fakeClient
	store   *store.Store
	broker  *sse.Broker
	clockMu sync.Mutex
	clock   time.Time
	path    string
	tmpl    store.Template
}

const token = "0123456789abcdef0123456789abcdef"

func testConfig() config.Config {
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Name: "host1", WorkerMachineControllerURL: "http://127.0.0.1:9480", ControlToken: token, MaxWorkers: 3}}
	return cfg
}

func newEnv(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monitor.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, client: newFakeClient(), store: st, broker: sse.NewBroker(), clock: t0, path: path}
	tmpl := store.DefaultTemplate()
	if e.tmpl, err = st.CreateTemplate(context.Background(), tmpl, t0); err != nil {
		t.Fatal(err)
	}
	e.f = e.fleet(st)
	return e
}

func (e *env) fleet(st *store.Store) *Fleet {
	e.t.Helper()
	f := New(testConfig(), st, map[string]Client{"host1": e.client}, e.broker, e.now)
	if err := f.Load(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	return f
}

// now is the test clock; the fleet's own goroutines read it too.
func (e *env) now() time.Time {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	return e.clock
}

// later is the clock d from now, without moving it.
func (e *env) later(d time.Duration) time.Time {
	return e.now().Add(d)
}

// poll moves the clock and polls the host once.
func (e *env) poll(d time.Duration) {
	e.t.Helper()
	e.clockMu.Lock()
	e.clock = e.clock.Add(d)
	e.clockMu.Unlock()
	e.f.PollHost(context.Background(), "host1")
}

func (e *env) view(id string) WorkerView {
	e.t.Helper()
	v, ok := e.f.Worker(id)
	if !ok {
		e.t.Fatalf("no worker %s in the view", id)
	}
	return v
}

func (e *env) events(id string) []store.Event {
	e.t.Helper()
	evs, err := e.store.Events(context.Background(), id, 0, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

// kinds are the kinds of a worker's events, oldest first.
func (e *env) kinds(id string) []string {
	evs := e.events(id)
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[len(evs)-1-i] = ev.Kind
	}
	return out
}

// createActive creates one worker and polls until it is ACTIVE.
func (e *env) createActive() string {
	e.t.Helper()
	res, err := e.f.Create(context.Background(), "host1", e.tmpl.ID, 1)
	if err != nil || len(res.Workers) != 1 || !res.Workers[0].OK {
		e.t.Fatalf("Create = %+v, %v", res, err)
	}
	id := res.Workers[0].ID
	e.client.set(id, ready(e.later(5*time.Second)))
	e.poll(5 * time.Second)
	if v := e.view(id); v.Lifecycle != "ACTIVE" || v.Health != "HEALTHY" {
		e.t.Fatalf("after the first good status: %s %s %v", v.Lifecycle, v.Health, v.HealthReasons)
	}
	return id
}

var errBoom = errors.New("boom")
