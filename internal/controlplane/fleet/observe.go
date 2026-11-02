package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	log "log/slog"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/health"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// ApplySnapshot applies one snapshot of a host: it adopts the managed containers the store does not know, follows
// the lifecycle and the health of each worker, writes the events and the history, and pushes what changed.
func (f *Fleet) ApplySnapshot(ctx context.Context, hostName string, snap controllerapi.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[hostName]
	if !ok {
		return
	}
	now := f.now()
	h.reachable, h.downSince = true, time.Time{}
	h.lastSeen = &now
	host := snap.Host
	h.metrics = &host
	h.controllerVersion = snap.WorkerMachineControllerVersion
	if now.Sub(h.persistedSeen) >= persistEvery {
		if err := f.store.SetHostSeen(ctx, hostName, now); err != nil {
			log.Error("failed to store the last seen time of a host", "host", hostName, "error", err)
		}
		h.persistedSeen = now
	}
	h.ring.Add(hostPoint(now, host, true))

	byID := map[string]*controllerapi.WorkerObservation{}
	for i := range snap.Workers {
		o := &snap.Workers[i]
		if !o.Managed || o.WorkerID == "" {
			continue
		}
		byID[o.WorkerID] = o
		if _, known := f.workers[o.WorkerID]; !known {
			f.adopt(ctx, hostName, o, now)
		}
	}
	for _, w := range f.workers {
		if w.row.Host == hostName {
			f.observe(ctx, w, byID[w.row.ID], now)
		}
	}
	f.publishHost(h)
}

// HostDown records that the worker machine controller of a host did not answer: its workers keep what was known for
// a short while, then become UNKNOWN (never DOWN: the workers may be fine).
func (f *Fleet) HostDown(ctx context.Context, hostName string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[hostName]
	if !ok {
		return
	}
	now := f.now()
	if h.reachable || h.downSince.IsZero() {
		log.Warn("a worker machine controller does not answer", "host", hostName, "error", err)
		// unreachable since the last snapshot it gave, not since this poll: the threshold counts from there
		h.downSince = now
		if h.lastSeen != nil {
			h.downSince = *h.lastSeen
		}
	}
	h.reachable = false
	h.ring.Add(HostPoint{T: now})
	for _, w := range f.workers {
		if w.row.Host != hostName {
			continue
		}
		f.applyHealth(ctx, w, health.Input{Now: now, Lifecycle: w.row.Lifecycle, ControllerDownSince: h.downSince,
			Observation: w.obs, Previous: w.res}, now)
		f.publishWorker(w)
	}
	f.publishHost(h)
}

// adopt adds a managed container the store does not know: the store was lost, or the container was created before
// the store was. It comes back as the monitor's own (L10 keeps external containers out).
func (f *Fleet) adopt(ctx context.Context, hostName string, o *controllerapi.WorkerObservation, now time.Time) {
	lc := lifecycle.Stopped
	if o.Container.State == "running" || o.Container.State == "restarting" {
		lc = lifecycle.Active
	}
	created := now
	if t, err := time.Parse(time.RFC3339, o.Container.Labels[workerapi.LabelCreatedAt]); err == nil {
		created = t
	}
	row := store.Worker{ID: o.WorkerID, Host: hostName, Managed: true, ContainerID: o.Container.ID, Lifecycle: lc,
		LifecycleSince: now, Health: string(health.Unknown), HealthReasons: []string{}, HealthSince: now, CreatedAt: created,
		TemplateSnapshot: json.RawMessage(`{}`)}
	if err := f.store.InsertWorker(ctx, row); err != nil {
		// a removed row of the same id: the container came back, so the row does too
		if old, gerr := f.store.Worker(ctx, o.WorkerID); gerr == nil && old.RemovedAt != nil {
			old.RemovedAt, old.Lifecycle, old.LifecycleReason, old.LifecycleSince, old.ContainerID = nil, lc, "", now, o.Container.ID
			row = old
			err = f.store.UpdateWorker(ctx, row)
		}
		if err != nil {
			log.Error("failed to adopt a worker container", "workerId", o.WorkerID, "error", err)
			return
		}
	}
	w := f.newWorker(row)
	f.workers[row.ID] = w
	f.addEvent(ctx, store.Event{WorkerID: row.ID, At: now, Kind: store.EventAdopted, To: lc,
		Message: "a container of the monitor that the control plane did not know was found on " + hostName})
}

// observe applies the observation of one worker (nil: its container is missing).
func (f *Fleet) observe(ctx context.Context, w *worker, o *controllerapi.WorkerObservation, now time.Time) {
	changed := false
	if o != nil && w.row.ContainerID != o.Container.ID {
		w.row.ContainerID = o.Container.ID
		changed = true
	}
	f.containerEvents(ctx, w, o, now)
	f.jobEvents(ctx, w, o, now)
	f.blockRates(w, o, now)
	w.obs = o

	before := w.row.Lifecycle
	next, reason, moved := lifecycle.Observe(before, lifecycleObservation(w, o, now, f.cfg.Health.ProvisionTimeoutSeconds))
	if moved {
		if next == lifecycle.ProvisionFailed {
			f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventProvisionFailed,
				Message: fmt.Sprintf("no good status within %d s", f.cfg.Health.ProvisionTimeoutSeconds)})
		}
		if next == lifecycle.Removed && reason == lifecycle.ReasonDisappeared {
			f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventDisappeared,
				Message: "the container was removed outside the monitor"})
		}
		f.setLifecycle(ctx, w, next, reason, "")
		if next == lifecycle.Removed {
			return
		}
		if before == lifecycle.Draining && reason == lifecycle.ReasonDrained && w.tmpl != nil && w.tmpl.RemoveAfterDrain {
			f.later(w.row.ID, lifecycle.ActionRemove, ActionOptions{RemoveVolumes: true})
		}
	}
	f.drainDeadline(ctx, w, now)

	restarted, err := f.store.CountEvents(ctx, w.row.ID, store.EventContainerRestarted,
		now.Add(-time.Duration(f.cfg.Health.RestartWindowSeconds)*time.Second))
	if err != nil {
		log.Error("failed to count the restarts of a worker", "workerId", w.row.ID, "error", err)
	}
	f.applyHealth(ctx, w, health.Input{Now: now, Lifecycle: w.row.Lifecycle, Observation: o,
		RestartedRecently: restarted > 0, Previous: w.res}, now)

	if o != nil && o.Telemetry.OK && o.Telemetry.Status != nil {
		w.row.LastSeenAt = &now
		s := o.Telemetry.Status
		if s.Version != "" && s.Version != w.row.Version {
			w.row.Version, changed = s.Version, true
		}
		if b, err := json.Marshal(s); err == nil {
			w.row.LastStatus = b
		}
	} else if o != nil && w.row.Version == "" {
		if _, tag, ok := strings.Cut(o.Container.Image, ":"); ok {
			w.row.Version, changed = tag, true
		}
	}
	if changed || now.Sub(w.persistedAt) >= persistEvery {
		f.persist(ctx, w)
	}
	if o != nil && o.Resources != nil {
		w.ring.Add(workerPoint(now, w, o))
	}
	f.publishWorker(w)
}

// applyHealth evaluates the health and records a change (of the health or of its reasons) with an event.
func (f *Fleet) applyHealth(ctx context.Context, w *worker, in health.Input, now time.Time) {
	r := health.Evaluate(in, f.cfg.Health)
	if !health.Same(r, w.res) {
		f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventHealthChanged,
			From: string(w.res.Health), To: string(r.Health), Reasons: r.Reasons, Message: r.Message})
		w.row.Health, w.row.HealthReasons, w.row.HealthSince = string(r.Health), r.Reasons, now
		w.res = r
		f.persist(ctx, w)
		return
	}
	w.res = r
}

// containerEvents records the restarts and the OOM kills seen since the previous poll.
func (f *Fleet) containerEvents(ctx context.Context, w *worker, o *controllerapi.WorkerObservation, now time.Time) {
	if o == nil {
		return
	}
	c := o.Container
	if w.seen {
		if c.RestartCount > w.restarts {
			f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventContainerRestarted,
				From: strconv.Itoa(w.restarts), To: strconv.Itoa(c.RestartCount),
				Message: fmt.Sprintf("Docker restarted the container (last exit code %d)", c.ExitCode)})
		}
		if c.OOMEvents > w.oomEvents || (c.OOMKilled && !w.oomKilled) {
			f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventOOMKilled,
				Message: "the container went over its memory limit"})
		}
	}
	w.seen, w.restarts, w.oomEvents, w.oomKilled = true, c.RestartCount, c.OOMEvents, c.OOMKilled
}

// jobEvents follows the job of the status: a new job id is JOB_STARTED, a job that is gone is JOB_ENDED with the
// outcome the worker counted. Only the id and the routine key are kept: the monitor knows nothing else of jobs.
func (f *Fleet) jobEvents(ctx context.Context, w *worker, o *controllerapi.WorkerObservation, now time.Time) {
	if o == nil || !o.Telemetry.OK || o.Telemetry.Status == nil {
		return
	}
	s := o.Telemetry.Status
	var current int64
	if s.Job != nil {
		current = s.Job.JobID
	}
	if w.outcomes != nil && current != w.jobID {
		if w.jobID != 0 {
			f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventJobEnded,
				From: strconv.FormatInt(w.jobID, 10), To: outcomeOf(w.outcomes, s.Counters.JobsByOutcome)})
		}
		if current != 0 {
			f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventJobStarted,
				To: strconv.FormatInt(current, 10), Message: s.Job.RoutineKey})
		}
	}
	w.jobID = current
	w.outcomes = map[string]int64{}
	maps.Copy(w.outcomes, s.Counters.JobsByOutcome)
}

// outcomeOf is the outcome whose counter went up, UNKNOWN when none did (e.g. the worker restarted).
func outcomeOf(before, after map[string]int64) string {
	for k, v := range after {
		if v > before[k] {
			return k
		}
	}
	return "UNKNOWN"
}

// blockRates turns the block I/O counters into rates; a counter going down (restart) gives no rate.
func (f *Fleet) blockRates(w *worker, o *controllerapi.WorkerObservation, now time.Time) {
	if o == nil || o.Resources == nil {
		w.blkReadRate, w.blkWriteRate, w.blkAt = nil, nil, time.Time{}
		return
	}
	r := o.Resources
	if !w.blkAt.IsZero() && now.After(w.blkAt) && r.BlockReadBytesTotal >= w.blkRead && r.BlockWriteBytesTotal >= w.blkWrite {
		secs := now.Sub(w.blkAt).Seconds()
		read, write := float64(r.BlockReadBytesTotal-w.blkRead)/secs, float64(r.BlockWriteBytesTotal-w.blkWrite)/secs
		w.blkReadRate, w.blkWriteRate = &read, &write
	} else {
		w.blkReadRate, w.blkWriteRate = nil, nil
	}
	w.blkRead, w.blkWrite, w.blkAt = r.BlockReadBytesTotal, r.BlockWriteBytesTotal, now
}

// drainDeadline escalates to a stop, or records a DRAIN_TIMEOUT once, when a drain went past its deadline.
func (f *Fleet) drainDeadline(ctx context.Context, w *worker, now time.Time) {
	if w.row.Lifecycle != lifecycle.Draining || w.row.DrainDeadline == nil || now.Before(*w.row.DrainDeadline) || w.drainTimeoutSent {
		return
	}
	w.drainTimeoutSent = true
	f.addEvent(ctx, store.Event{WorkerID: w.row.ID, At: now, Kind: store.EventDrainTimeout,
		Message: fmt.Sprintf("the drain is past its deadline of %s", w.row.DrainDeadline.Format(time.RFC3339))})
	if w.tmpl != nil && w.tmpl.EscalateAfterDrainTimeout {
		f.later(w.row.ID, lifecycle.ActionStop, ActionOptions{reason: lifecycle.ReasonDrainTimeout})
	}
}

// later runs an action of the fleet itself, outside the poll.
func (f *Fleet) later(id, action string, opts ActionOptions) {
	f.background.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
		defer cancel()
		if _, err := f.Act(ctx, id, action, opts); err != nil {
			log.Error("an action of the control plane failed", "workerId", id, "action", action, "error", err)
		}
	})
}

func lifecycleObservation(w *worker, o *controllerapi.WorkerObservation, now time.Time, provisionTimeout int) lifecycle.Observation {
	lo := lifecycle.Observation{Now: now, LifecycleSince: w.row.LifecycleSince, Missing: o == nil,
		ProvisionTimeout: time.Duration(provisionTimeout) * time.Second}
	if o == nil {
		return lo
	}
	c := o.Container
	lo.Running = c.State == "running"
	lo.Exited = c.State == "exited" || c.State == "dead"
	lo.ExitCode = c.ExitCode
	lo.StatusOK = o.Telemetry.OK && o.Telemetry.Status != nil
	lo.TelemetryConfigured = o.Telemetry.Configured
	if c.StartedAt != nil {
		lo.RunningSince = *c.StartedAt
	}
	return lo
}

func workerPoint(now time.Time, w *worker, o *controllerapi.WorkerObservation) WorkerPoint {
	r := o.Resources
	return WorkerPoint{T: now, CPUCores: r.CPUCores, CPULimit: o.Container.Limits.CPUs, MemWorkingSet: r.MemoryWorkingSetBytes,
		MemLimit: o.Container.Limits.MemoryBytes, Pids: r.Pids, BlkReadRate: w.blkReadRate, BlkWriteRate: w.blkWriteRate,
		Health: string(w.res.Health)}
}

func hostPoint(now time.Time, m controllerapi.HostMetrics, reachable bool) HostPoint {
	p := HostPoint{T: now, CPURatio: m.CPUUsageRatio, MemUsed: m.MemoryTotalBytes - m.MemoryAvailableBytes,
		MemTotal: m.MemoryTotalBytes, Load1: m.Load1, Reachable: reachable}
	for _, d := range m.Disks {
		if d.TotalBytes > 0 {
			p.Disks = append(p.Disks, DiskPoint{Path: d.Path, UsedRatio: float64(d.TotalBytes-d.FreeBytes) / float64(d.TotalBytes)})
		}
	}
	for _, n := range m.Networks {
		p.NetRx += n.RxBytesPerSec
		p.NetTx += n.TxBytesPerSec
	}
	return p
}
