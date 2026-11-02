package fleet

import (
	"context"
	"errors"
	log "log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/history"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
)

// The helpers below up to FlushHistory are called with f.mu held.

// rollover writes the finished minute of every worker and host when now is in a new minute.
func (f *Fleet) rollover(ctx context.Context, now time.Time) {
	ws, hs := f.acc.Rollover(now)
	f.writeSamples(ctx, ws, hs)
}

func (f *Fleet) writeSamples(ctx context.Context, ws []store.WorkerSample, hs []store.HostSample) {
	if len(ws)+len(hs) == 0 {
		return
	}
	if err := f.store.WriteSamples(ctx, ws, hs); err != nil {
		log.Error("failed to write the minute samples", "workers", len(ws), "hosts", len(hs), "error", err)
	}
}

// sample adds the poll of a worker to its minute (o nil: the poll did not see the worker). A stopped or failed worker
// has no rows: no data, not zeros.
func (f *Fleet) sample(w *worker, o *controllerapi.WorkerObservation, now time.Time) {
	switch w.row.Lifecycle {
	case lifecycle.Stopped, lifecycle.Removed, lifecycle.ProvisionFailed:
		return
	}
	p := history.WorkerPoll{Health: string(w.res.Health), Responsive: w.res.Responsive, Busy: o != nil && busy(w)}
	if o != nil && o.Resources != nil {
		r := o.Resources
		p.CPUCores, p.MemWorkingSet, p.Pids = &r.CPUCores, &r.MemoryWorkingSetBytes, &r.Pids
		p.CPULimit, p.MemLimit = o.Container.Limits.CPUs, o.Container.Limits.MemoryBytes
		p.BlkRead, p.BlkWrite = w.blkReadRate, w.blkWriteRate
	}
	if o != nil && o.Telemetry.OK && o.Telemetry.Status != nil {
		s := o.Telemetry.Status
		p.Phase = string(s.Phase)
		if s.Job != nil {
			p.JobID, p.Routine = &s.Job.JobID, &s.Job.RoutineKey
		}
	}
	f.acc.Worker(w.row.ID, now, p)
}

func hostPoll(p HostPoint) history.HostPoll {
	hp := history.HostPoll{Reachable: p.Reachable}
	if !p.Reachable {
		return hp
	}
	hp.CPURatio, hp.MemUsed, hp.MemTotal, hp.Load1, hp.NetRx, hp.NetTx = &p.CPURatio, &p.MemUsed, &p.MemTotal, &p.Load1, &p.NetRx, &p.NetTx
	for _, d := range p.Disks {
		hp.Disks = append(hp.Disks, store.DiskMax{Path: d.Path, UsedRatioMax: d.UsedRatio})
	}
	return hp
}

// FlushHistory writes the minute in progress, at the shutdown: the rest of the minute merges with it after a restart.
func (f *Fleet) FlushHistory(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws, hs := f.acc.Drain()
	f.writeSamples(ctx, ws, hs)
}

// knownWorker tells whether a worker is in the fleet, or was: a removed worker keeps its history until the purge.
func (f *Fleet) knownWorker(ctx context.Context, id string) (bool, error) {
	f.mu.Lock()
	_, ok := f.workers[id]
	f.mu.Unlock()
	if ok {
		return true, nil
	}
	if _, err := f.store.Worker(ctx, id); err == nil {
		return true, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	return f.store.HasWorkerSamples(ctx, id)
}

// Availability is how a worker was over a range (plan/09 §1.5), from its minute samples.
type Availability struct {
	Polls int64 `json:"polls"`
	// responsive polls / polls and busy polls / polls; null without polls
	Availability *float64 `json:"availability"`
	Utilisation  *float64 `json:"utilisation"`
}

// the ranges of the availability
var availabilityRanges = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

func availabilityOf(u store.Usage) Availability {
	a := Availability{Polls: u.Polls}
	if u.Polls > 0 {
		avail, util := float64(u.Responsive)/float64(u.Polls), float64(u.Busy)/float64(u.Polls)
		a.Availability, a.Utilisation = &avail, &util
	}
	return a
}

func (f *Fleet) usages(ctx context.Context, rng string) (map[string]store.Usage, error) {
	d, ok := availabilityRanges[rng]
	if !ok {
		return nil, errorf(http.StatusBadRequest, "INVALID", "range must be 24h or 7d")
	}
	u, err := f.store.Usages(ctx, history.Minute(f.now().Add(-d)))
	if err != nil {
		return nil, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the history: %v", err)
	}
	return u, nil
}

// WorkerAvailability returns the availability and the utilisation of one worker over 24h or 7d.
func (f *Fleet) WorkerAvailability(ctx context.Context, id, rng string) (Availability, error) {
	u, err := f.usages(ctx, rng)
	if err != nil {
		return Availability{}, err
	}
	if ok, err := f.knownWorker(ctx, id); err != nil {
		return Availability{}, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the worker: %v", err)
	} else if !ok {
		return Availability{}, errorf(http.StatusNotFound, "NOT_FOUND", "no worker %s", id)
	}
	return availabilityOf(u[id]), nil
}

// FleetAvailability returns the availability of every worker with history in the range, by worker id: the column
// "avail 24h" of the fleet table.
func (f *Fleet) FleetAvailability(ctx context.Context, rng string) (map[string]Availability, error) {
	u, err := f.usages(ctx, rng)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Availability, len(u))
	for id, usage := range u {
		out[id] = availabilityOf(usage)
	}
	return out, nil
}

// FleetSeriesNames are the series of the fleet chart.
var FleetSeriesNames = []string{"capacity", "jobs"}

// FleetTimeseries returns the fleet over time from the minute samples: how many workers were active, busy and
// responsive (averaged over each bucket), and the jobs that ended by outcome.
func (f *Fleet) FleetTimeseries(ctx context.Context, series []string, rng string) (Timeseries, error) {
	spec, err := rangeOf(rng)
	if err != nil {
		return Timeseries{}, err
	}
	if err := checkSeries(series, FleetSeriesNames); err != nil {
		return Timeseries{}, err
	}
	now := f.now()
	g := newGrid(now.Add(-spec.d), now, max(spec.bucket, 60))
	out := Timeseries{Source: "samples", BucketSeconds: g.bucket, Series: []Series{}}
	for _, name := range series {
		switch name {
		case "capacity":
			minutes, err := f.store.FleetMinutes(ctx, g.from, g.to)
			if err != nil {
				return Timeseries{}, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the history: %v", err)
			}
			type sum struct {
				n                        int
				active, busy, responsive float64
			}
			sums := map[int64]*sum{}
			for _, m := range minutes {
				b := g.of(m.Minute)
				if sums[b] == nil {
					sums[b] = &sum{}
				}
				s := sums[b]
				s.n++
				s.active, s.busy, s.responsive = s.active+float64(m.Active), s.busy+m.Busy, s.responsive+m.Responsive
			}
			for _, l := range []struct {
				name  string
				value func(*sum) float64
			}{{"active", func(s *sum) float64 { return s.active }}, {"busy", func(s *sum) float64 { return s.busy }},
				{"responsive", func(s *sum) float64 { return s.responsive }}} {
				out.Series = append(out.Series, g.series(l.name, "workers", func(b int64) any {
					s := sums[b]
					if s == nil {
						return nil
					}
					return l.value(s) / float64(s.n)
				}))
			}
		case "jobs":
			jobs, err := f.jobSeries(ctx, "", g)
			if err != nil {
				return Timeseries{}, err
			}
			out.Series = append(out.Series, jobs...)
		}
	}
	return out, nil
}

// jobSeries counts the jobs that ended in each bucket, one series per outcome ("jobs:FINISHED"); a bucket without jobs
// is 0, not a gap.
func (f *Fleet) jobSeries(ctx context.Context, workerID string, g grid) ([]Series, error) {
	ended, err := f.store.JobsEnded(ctx, workerID, time.Unix(g.from, 0), time.Unix(g.to+int64(g.bucket), 0))
	if err != nil {
		return nil, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the jobs: %v", err)
	}
	counts := map[string]map[int64]int{}
	for _, j := range ended {
		if counts[j.Outcome] == nil {
			counts[j.Outcome] = map[int64]int{}
		}
		counts[j.Outcome][g.of(j.At.Unix())]++
	}
	outcomes := make([]string, 0, len(counts))
	for o := range counts {
		outcomes = append(outcomes, o)
	}
	slices.Sort(outcomes)
	out := []Series{}
	for _, o := range outcomes {
		out = append(out, g.series("jobs:"+o, "count", func(b int64) any { return counts[o][b] }))
	}
	return out, nil
}

// grid is the buckets of a chart: from the bucket of the start to the bucket of now, every one of them a point.
type grid struct {
	from, to int64 // the first and the last bucket, unix seconds
	bucket   int
}

func newGrid(since, now time.Time, bucket int) grid {
	b := int64(bucket)
	return grid{from: since.Unix() / b * b, to: now.Unix() / b * b, bucket: bucket}
}

// of is the bucket of a time in unix seconds.
func (g grid) of(t int64) int64 {
	return t / int64(g.bucket) * int64(g.bucket)
}

func (g grid) series(name, unit string, value func(bucket int64) any) Series {
	s := Series{Name: name, Unit: unit, Points: [][]any{}}
	for b := g.from; b <= g.to; b += int64(g.bucket) {
		s.Points = append(s.Points, []any{b, value(b)})
	}
	return s
}
