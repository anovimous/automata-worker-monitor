// Package stats streams the Docker stats of the running worker containers and turns them into the resources of the
// snapshot (plan/06-worker-machine-controller.md §4).
package stats

import (
	"context"
	log "log/slog"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
)

// a stream that fails is opened again after a backoff, while the container runs
const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// a pids limit at or above this is Docker's "max", no limit
const noPidsLimit = 1 << 62

// Compute turns a sample into the resources of the snapshot; prev is the sample before it (nil for the first one,
// which gives no CPU rate). The network fields stay null: per-worker network stats are not built (R3 is delayed).
func Compute(prev *docker.Sample, cur docker.Sample) controllerapi.Resources {
	r := controllerapi.Resources{
		SampledAt:                cur.Read.UTC(),
		CPUUsageSecondsTotal:     float64(cur.CPUTotalNs) / 1e9,
		CPUThrottledSecondsTotal: float64(cur.CPUThrottledNs) / 1e9,
		MemoryUsageBytes:         int64(cur.MemUsage),
		MemoryLimitBytes:         int64(cur.MemLimit),
		Pids:                     int64(cur.Pids),
		BlockReadBytesTotal:      int64(cur.BlockRead),
		BlockWriteBytesTotal:     int64(cur.BlockWrite),
	}
	// the working set is what docker stats shows: the page cache that can be dropped is not counted
	if cur.MemUsage > cur.MemInactiveFile {
		r.MemoryWorkingSetBytes = int64(cur.MemUsage - cur.MemInactiveFile)
	}
	if cur.PidsLimit > 0 && cur.PidsLimit < noPidsLimit {
		limit := int64(cur.PidsLimit)
		r.PidsLimit = &limit
	}
	// cores in use: CPU time over wall time between two samples, comparable with --cpus
	if prev != nil && cur.Read.After(prev.Read) && cur.CPUTotalNs >= prev.CPUTotalNs {
		r.CPUCores = float64(cur.CPUTotalNs-prev.CPUTotalNs) / float64(cur.Read.Sub(prev.Read).Nanoseconds())
	}
	return r
}

// Manager keeps one stats stream per running worker container, and the latest resources of each.
type Manager struct {
	engine docker.Engine

	mu      sync.Mutex
	streams map[string]context.CancelFunc
	latest  map[string]controllerapi.Resources
}

func NewManager(engine docker.Engine) *Manager {
	return &Manager{engine: engine, streams: map[string]context.CancelFunc{}, latest: map[string]controllerapi.Resources{}}
}

// Sync starts a stream for each running container that has none and stops those of the others.
func (m *Manager) Sync(ctx context.Context, running []string) {
	want := map[string]bool{}
	for _, id := range running {
		want[id] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, cancel := range m.streams {
		if !want[id] {
			cancel()
			delete(m.streams, id)
			delete(m.latest, id)
		}
	}
	for id := range want {
		if _, ok := m.streams[id]; ok {
			continue
		}
		streamCtx, cancel := context.WithCancel(ctx)
		m.streams[id] = cancel
		go m.follow(streamCtx, id)
	}
}

// Get returns the latest resources of a container; ok is false before the first sample.
func (m *Manager) Get(containerID string) (controllerapi.Resources, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.latest[containerID]
	return r, ok
}

// Stop ends every stream.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, cancel := range m.streams {
		cancel()
		delete(m.streams, id)
	}
}

func (m *Manager) follow(ctx context.Context, id string) {
	backoff := minBackoff
	for ctx.Err() == nil {
		samples, err := m.engine.Stats(ctx, id)
		if err != nil {
			log.Debug("failed to open the stats stream", "containerId", id, "error", err)
		} else {
			var prev *docker.Sample
			for s := range samples {
				r := Compute(prev, s)
				m.store(ctx, id, r)
				prev = &s
				backoff = minBackoff
			}
		}
		// the stream ended: the container stopped or the daemon broke it; no sample is better than an old one
		m.clear(ctx, id)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

func (m *Manager) store(ctx context.Context, id string, r controllerapi.Resources) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// a stream stopped by Sync must not write after it
	if ctx.Err() == nil {
		m.latest[id] = r
	}
}

func (m *Manager) clear(ctx context.Context, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() == nil {
		delete(m.latest, id)
	}
}
