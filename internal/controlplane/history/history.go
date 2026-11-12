// Package history turns the polls into the minute samples kept in SQLite: the running
// sums and maxima of the current minute, per worker and per host, written all at once when a poll falls into the
// next minute.
package history

import (
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
)

// WorkerPoll is what one poll saw of a worker. A nil value is one the poll did not have.
type WorkerPoll struct {
	CPUCores, CPULimit *float64
	MemWorkingSet      *int64
	MemLimit           *int64
	Pids               *int64
	BlkRead, BlkWrite  *float64 // rates since the previous poll
	Health             string
	Responsive         bool
	Busy               bool
	Phase              string
	JobID              *int64
	Routine            *string
}

// HostPoll is what one poll saw of a host.
type HostPoll struct {
	Reachable    bool
	CPURatio     *float64
	MemUsed      *int64
	MemTotal     *int64
	Load1        *float64
	Disks        []store.DiskMax // the used ratio of each path at this poll
	NetRx, NetTx *float64
}

// Accumulator holds the current minute. It is safe for concurrent use.
type Accumulator struct {
	mu      sync.Mutex
	minute  int64
	workers map[string]store.WorkerSample
	hosts   map[string]store.HostSample
}

func New() *Accumulator {
	return &Accumulator{workers: map[string]store.WorkerSample{}, hosts: map[string]store.HostSample{}}
}

// Minute floors a time to its minute, in unix seconds.
func Minute(t time.Time) int64 {
	return t.Unix() / 60 * 60
}

// Rollover ends the current minute when at is in a later one: it returns the finished samples (empty when there are
// none) and starts the minute of at.
func (a *Accumulator) Rollover(at time.Time) ([]store.WorkerSample, []store.HostSample) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := Minute(at)
	if m <= a.minute {
		return nil, nil
	}
	a.minute = m
	return a.drain()
}

// Drain ends the current minute whatever the time, e.g. at the shutdown: the rest of the minute merges with it later.
func (a *Accumulator) Drain() ([]store.WorkerSample, []store.HostSample) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.drain()
}

func (a *Accumulator) drain() ([]store.WorkerSample, []store.HostSample) {
	var ws []store.WorkerSample
	for _, s := range a.workers {
		ws = append(ws, s)
	}
	var hs []store.HostSample
	for _, s := range a.hosts {
		hs = append(hs, s)
	}
	a.workers, a.hosts = map[string]store.WorkerSample{}, map[string]store.HostSample{}
	return ws, hs
}

// Worker adds one poll of a worker to its minute.
func (a *Accumulator) Worker(id string, at time.Time, p WorkerPoll) {
	s := store.WorkerSample{WorkerID: id, Minute: Minute(at), Polls: 1, CPUAvg: p.CPUCores, CPUMax: p.CPUCores,
		CPULimit: p.CPULimit, MemAvg: p.MemWorkingSet, MemMax: p.MemWorkingSet, MemLimit: p.MemLimit, PidsMax: p.Pids,
		BlkReadBps: p.BlkRead, BlkWriteBps: p.BlkWrite, HealthWorst: p.Health, PhaseLast: p.Phase, JobIDLast: p.JobID,
		RoutineLast: p.Routine}
	if p.CPUCores != nil || p.MemWorkingSet != nil {
		s.ResourcePolls = 1
	}
	if p.BlkRead != nil || p.BlkWrite != nil {
		s.RatePolls = 1
	}
	if p.Responsive {
		s.ResponsivePolls = 1
	}
	if p.Busy {
		s.BusyPolls = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if old, ok := a.workers[id]; ok && old.Minute == s.Minute {
		s = store.MergeWorkerSamples(old, s)
	}
	a.workers[id] = s
}

// Host adds one poll of a host to its minute; a host that did not answer counts as a poll, not as a reachable one.
func (a *Accumulator) Host(name string, at time.Time, p HostPoll) {
	s := store.HostSample{Host: name, Minute: Minute(at), Polls: 1}
	if p.Reachable {
		s.ReachablePolls = 1
		s.CPUAvg, s.CPUMax, s.MemUsedAvg, s.MemTotal, s.Load1Avg = p.CPURatio, p.CPURatio, p.MemUsed, p.MemTotal, p.Load1
		s.Disks, s.NetRxBps, s.NetTxBps = p.Disks, p.NetRx, p.NetTx
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if old, ok := a.hosts[name]; ok && old.Minute == s.Minute {
		s = store.MergeHostSamples(old, s)
	}
	a.hosts[name] = s
}
