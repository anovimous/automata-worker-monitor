// Package fleet is the live picture of the workers: it applies the snapshots of the worker machine controllers
// (reconciliation, health, lifecycle, events, history in memory), runs the operator's actions and provisions new
// workers (plan/07-workers-control-plane.md §3–§6). The API reads its views; the store keeps what must survive a
// restart.
package fleet

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	log "log/slog"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/controllerclient"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/health"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/ringbuf"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/sse"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
)

// Client is what the fleet does with a worker machine controller (controllerclient.Client).
type Client interface {
	Snapshot(ctx context.Context) (controllerapi.Snapshot, error)
	Worker(ctx context.Context, id string) (controllerapi.WorkerDetail, error)
	Create(ctx context.Context, req controllerapi.CreateWorkerRequest) (controllerapi.CreateWorkerResponse, error)
	Start(ctx context.Context, id string) (controllerapi.WorkerObservation, error)
	Signal(ctx context.Context, id, signal string) (controllerapi.WorkerObservation, error)
	Stop(ctx context.Context, id string, timeout time.Duration) (controllerapi.WorkerObservation, error)
	Kill(ctx context.Context, id string) (controllerapi.WorkerObservation, error)
	Restart(ctx context.Context, id string, timeout time.Duration) (controllerapi.WorkerObservation, error)
	Remove(ctx context.Context, id string, removeVolumes bool) (controllerapi.RemoveResponse, error)
	Logs(ctx context.Context, id string, opts controllerclient.LogOptions) (io.ReadCloser, string, error)
	EnvFiles(ctx context.Context) ([]string, error)
	Images(ctx context.Context, reference string) ([]controllerapi.Image, error)
}

// the last seen time and the last status are written to the store at most this often, unless something shown changed
const persistEvery = 30 * time.Second

type Fleet struct {
	cfg     config.Config
	store   *store.Store
	clients map[string]Client
	broker  *sse.Broker
	now     func() time.Time
	random  io.Reader

	mu      sync.Mutex
	workers map[string]*worker
	hosts   map[string]*host

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
	// one create at a time, so that the maxWorkers check holds
	createMu sync.Mutex
	// the actions started by the fleet itself (drain escalation, removal after a drain)
	background sync.WaitGroup
}

type worker struct {
	row  store.Worker
	tmpl *store.Template // the template as used at the creation, nil when unknown (re-adopted)
	obs  *controllerapi.WorkerObservation
	res  health.Result

	seen      bool // observed since the control plane started: the baselines below are set
	restarts  int
	oomEvents int
	oomKilled bool
	jobID     int64
	outcomes  map[string]int64

	blkRead, blkWrite         int64
	blkAt                     time.Time
	blkReadRate, blkWriteRate *float64

	ring             *ringbuf.Ring[WorkerPoint]
	drainTimeoutSent bool
	persistedAt      time.Time
	published        []byte
}

type host struct {
	cfg               config.Host
	reachable         bool
	downSince         time.Time
	lastSeen          *time.Time
	persistedSeen     time.Time
	metrics           *controllerapi.HostMetrics
	controllerVersion string
	cordoned          bool
	cordonedReason    string
	ring              *ringbuf.Ring[HostPoint]
	published         []byte
}

// WorkerPoint is one point of the live history of a worker.
type WorkerPoint struct {
	T             time.Time
	CPUCores      float64
	CPULimit      *float64
	MemWorkingSet int64
	MemLimit      *int64
	Pids          int64
	BlkReadRate   *float64
	BlkWriteRate  *float64
	Health        string
}

// HostPoint is one point of the live history of a host.
type HostPoint struct {
	T         time.Time
	CPURatio  float64
	MemUsed   int64
	MemTotal  int64
	Load1     float64
	Disks     []DiskPoint
	NetRx     float64
	NetTx     float64
	Reachable bool
}

type DiskPoint struct {
	Path      string
	UsedRatio float64
}

func New(cfg config.Config, st *store.Store, clients map[string]Client, broker *sse.Broker, now func() time.Time) *Fleet {
	return &Fleet{cfg: cfg, store: st, clients: clients, broker: broker, now: now, random: rand.Reader,
		workers: map[string]*worker{}, hosts: map[string]*host{}, locks: map[string]*sync.Mutex{}}
}

// Load reads the hosts and the workers of the store, so that the picture is there before the first poll.
func (f *Fleet) Load(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, hc := range f.cfg.Hosts {
		st, err := f.store.HostState(ctx, hc.Name)
		if err != nil {
			return fmt.Errorf("failed to read the state of host %s: %w", hc.Name, err)
		}
		f.hosts[hc.Name] = &host{cfg: hc, lastSeen: st.LastSeenAt, cordoned: st.Cordoned, cordonedReason: st.CordonedReason,
			ring: ringbuf.New[HostPoint](ringbuf.Size)}
	}
	rows, err := f.store.Workers(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to read the workers: %w", err)
	}
	for _, row := range rows {
		if _, ok := f.hosts[row.Host]; !ok {
			log.Warn("a worker of a host that is no longer configured is left out", "workerId", row.ID, "host", row.Host)
			continue
		}
		f.workers[row.ID] = f.newWorker(row)
	}
	return nil
}

func (f *Fleet) newWorker(row store.Worker) *worker {
	w := &worker{row: row, ring: ringbuf.New[WorkerPoint](ringbuf.Size),
		res: health.Result{Health: health.Health(row.Health), Reasons: row.HealthReasons, Candidate: health.Health(row.Health)}}
	var t store.Template
	if len(row.TemplateSnapshot) > 0 && json.Unmarshal(row.TemplateSnapshot, &t) == nil && t.Name != "" {
		w.tmpl = &t
	}
	return w
}

// lock serializes the actions of one worker, so that two clicks can not race.
func (f *Fleet) lock(id string) func() {
	f.locksMu.Lock()
	l, ok := f.locks[id]
	if !ok {
		l = &sync.Mutex{}
		f.locks[id] = l
	}
	f.locksMu.Unlock()
	l.Lock()
	return l.Unlock
}

// Wait waits for the actions the fleet started by itself.
func (f *Fleet) Wait() {
	f.background.Wait()
}

// The helpers below are called with f.mu held.

// addEvent stores an event and pushes it to the browsers.
func (f *Fleet) addEvent(ctx context.Context, e store.Event) {
	e, err := f.store.AddEvent(ctx, e)
	if err != nil {
		log.Error("failed to store an event", "workerId", e.WorkerID, "kind", e.Kind, "error", err)
		return
	}
	f.broker.Publish(sse.Event{Type: "worker.event", Data: e})
}

// persist writes the row of a worker.
func (f *Fleet) persist(ctx context.Context, w *worker) {
	if err := f.store.UpdateWorker(ctx, w.row); err != nil {
		log.Error("failed to store a worker", "workerId", w.row.ID, "error", err)
		return
	}
	w.persistedAt = f.now()
}

// setLifecycle moves a worker to a lifecycle, with an event; a removed worker leaves the view.
func (f *Fleet) setLifecycle(ctx context.Context, w *worker, lc, reason, message string) {
	if w.row.Lifecycle == lc && w.row.LifecycleReason == reason {
		return
	}
	now := f.now()
	f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventLifecycleChanged,
		From: w.row.Lifecycle, To: lc, Reasons: nonEmpty(reason), Message: message})
	w.row.Lifecycle, w.row.LifecycleReason, w.row.LifecycleSince = lc, reason, now
	if lc != lifecycle.Draining {
		w.row.DrainDeadline = nil
		w.drainTimeoutSent = false
	}
	if lc == lifecycle.Removed {
		w.row.RemovedAt = &now
		f.persist(ctx, w)
		delete(f.workers, w.row.ID)
		f.broker.Publish(sse.Event{Type: "worker.removed", Data: map[string]string{"id": w.row.ID}})
		return
	}
	f.persist(ctx, w)
}

// publishWorker pushes the view of a worker when it changed since the last push.
func (f *Fleet) publishWorker(w *worker) {
	v := f.viewOf(w)
	b, err := json.Marshal(v)
	if err != nil || string(b) == string(w.published) {
		return
	}
	w.published = b
	f.broker.Publish(sse.Event{Type: "worker.updated", Data: v})
}

func (f *Fleet) publishHost(h *host) {
	v := f.hostViewOf(h)
	b, err := json.Marshal(v)
	if err != nil || string(b) == string(h.published) {
		return
	}
	h.published = b
	f.broker.Publish(sse.Event{Type: "host.updated", Data: v})
}

func nonEmpty(s string) []string {
	if s == "" {
		return []string{}
	}
	return []string{s}
}
