package fleet

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controlplane/history"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
)

// Series is one line of a chart; a point is [unix seconds, value or null].
type Series struct {
	Name   string  `json:"name"`
	Unit   string  `json:"unit"`
	Points [][]any `json:"points"`
}

// Timeseries is the answer of the timeseries routes.
type Timeseries struct {
	Source        string   `json:"source"` // buffer (the 5 s points) or samples (the minute samples)
	BucketSeconds int      `json:"bucketSeconds"`
	Series        []Series `json:"series"`
}

type rangeSpec struct {
	d      time.Duration
	bucket int // seconds; 0: the live buffer, one point per poll
}

// the ranges of the charts: the short ones from the live buffer, the long ones from the minute samples, with buckets
// that keep a chart around 300 points
var ranges = map[string]rangeSpec{
	"15m": {d: 15 * time.Minute}, "1h": {d: time.Hour},
	"6h": {d: 6 * time.Hour, bucket: 60}, "24h": {d: 24 * time.Hour, bucket: 300},
	"7d": {d: 7 * 24 * time.Hour, bucket: 1800}, "14d": {d: 14 * 24 * time.Hour, bucket: 3600},
}

// WorkerSeriesNames and HostSeriesNames are the series a chart may ask for.
var (
	WorkerSeriesNames = []string{"cpu", "memory", "pids", "io", "health", "jobs"}
	HostSeriesNames   = []string{"cpu", "memory", "load", "disk", "net"}
)

func rangeOf(r string) (rangeSpec, error) {
	spec, ok := ranges[r]
	if !ok {
		return rangeSpec{}, errorf(http.StatusBadRequest, "INVALID", "range must be one of 15m, 1h, 6h, 24h, 7d, 14d")
	}
	return spec, nil
}

func checkSeries(asked, known []string) error {
	for _, s := range asked {
		if !slices.Contains(known, s) {
			return errorf(http.StatusBadRequest, "INVALID", "unknown series %q, the series are %v", s, known)
		}
	}
	return nil
}

// fromBuffer tells whether a short range is served from the live buffer. Right after a restart of the control plane
// the buffer does not cover the range yet: when minute samples from before the start do, they serve it instead.
func (f *Fleet) fromBuffer(ctx context.Context, spec rangeSpec, since time.Time, before func(ctx context.Context, from, to int64) (bool, error)) (bool, error) {
	if spec.bucket != 0 {
		return false, nil
	}
	if !f.started.After(since) {
		return true, nil
	}
	older, err := before(ctx, history.Minute(since), f.started.Unix()-1)
	if err != nil {
		return false, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the history: %v", err)
	}
	return !older, nil
}

// WorkerTimeseries returns the history of a worker, also of one removed since (until the purge).
func (f *Fleet) WorkerTimeseries(ctx context.Context, id string, series []string, rng string) (Timeseries, error) {
	spec, err := rangeOf(rng)
	if err != nil {
		return Timeseries{}, err
	}
	if err := checkSeries(series, WorkerSeriesNames); err != nil {
		return Timeseries{}, err
	}
	if ok, err := f.knownWorker(ctx, id); err != nil {
		return Timeseries{}, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the worker: %v", err)
	} else if !ok {
		return Timeseries{}, errorf(http.StatusNotFound, "NOT_FOUND", "no worker %s", id)
	}
	now := f.now()
	since := now.Add(-spec.d)
	buffer, err := f.fromBuffer(ctx, spec, since, func(ctx context.Context, from, to int64) (bool, error) {
		rows, err := f.store.WorkerSamples(ctx, id, from, to)
		return len(rows) > 0, err
	})
	if err != nil {
		return Timeseries{}, err
	}
	if !buffer {
		return f.workerSamples(ctx, id, series, newGrid(since, now, max(spec.bucket, 60)))
	}

	f.mu.Lock()
	var points []WorkerPoint
	if w, ok := f.workers[id]; ok {
		for _, p := range w.ring.Points() {
			if !p.T.Before(since) {
				points = append(points, p)
			}
		}
	}
	f.mu.Unlock()

	out := Timeseries{Source: "buffer", BucketSeconds: f.cfg.Polling.IntervalSeconds, Series: []Series{}}
	line := func(name, unit string, value func(WorkerPoint) any) {
		s := Series{Name: name, Unit: unit, Points: [][]any{}}
		for _, p := range points {
			s.Points = append(s.Points, []any{p.T.Unix(), value(p)})
		}
		out.Series = append(out.Series, s)
	}
	for _, name := range series {
		switch name {
		case "cpu":
			line("cpu", "cores", func(p WorkerPoint) any { return p.CPUCores })
			line("cpuLimit", "cores", func(p WorkerPoint) any { return orNil(p.CPULimit) })
		case "memory":
			line("memory", "bytes", func(p WorkerPoint) any { return p.MemWorkingSet })
			line("memoryLimit", "bytes", func(p WorkerPoint) any { return orNil(p.MemLimit) })
		case "pids":
			line("pids", "count", func(p WorkerPoint) any { return p.Pids })
		case "io":
			line("blockRead", "bytes/s", func(p WorkerPoint) any { return orNil(p.BlkReadRate) })
			line("blockWrite", "bytes/s", func(p WorkerPoint) any { return orNil(p.BlkWriteRate) })
		case "health":
			line("health", "health", func(p WorkerPoint) any { return p.Health })
		case "jobs":
			jobs, err := f.jobSeries(ctx, id, newGrid(since, now, out.BucketSeconds))
			if err != nil {
				return Timeseries{}, err
			}
			out.Series = append(out.Series, jobs...)
		}
	}
	return out, nil
}

// workerSamples serves a worker's chart from its minute samples, merged by bucket: averages weighted by the polls,
// maxima of the maxima, the worst health. A bucket without rows is a gap (null).
func (f *Fleet) workerSamples(ctx context.Context, id string, series []string, g grid) (Timeseries, error) {
	rows, err := f.store.WorkerSamples(ctx, id, g.from, g.to+int64(g.bucket)-1)
	if err != nil {
		return Timeseries{}, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the history: %v", err)
	}
	buckets := map[int64]store.WorkerSample{}
	for _, r := range rows {
		b := g.of(r.Minute)
		if old, ok := buckets[b]; ok {
			r = store.MergeWorkerSamples(old, r)
		}
		buckets[b] = r
	}

	out := Timeseries{Source: "samples", BucketSeconds: g.bucket, Series: []Series{}}
	line := func(name, unit string, value func(store.WorkerSample) any) {
		out.Series = append(out.Series, g.series(name, unit, func(b int64) any {
			s, ok := buckets[b]
			if !ok {
				return nil
			}
			return value(s)
		}))
	}
	for _, name := range series {
		switch name {
		case "cpu":
			line("cpu", "cores", func(s store.WorkerSample) any { return orNil(s.CPUAvg) })
			line("cpuMax", "cores", func(s store.WorkerSample) any { return orNil(s.CPUMax) })
			line("cpuLimit", "cores", func(s store.WorkerSample) any { return orNil(s.CPULimit) })
		case "memory":
			line("memory", "bytes", func(s store.WorkerSample) any { return orNil(s.MemAvg) })
			line("memoryMax", "bytes", func(s store.WorkerSample) any { return orNil(s.MemMax) })
			line("memoryLimit", "bytes", func(s store.WorkerSample) any { return orNil(s.MemLimit) })
		case "pids":
			line("pids", "count", func(s store.WorkerSample) any { return orNil(s.PidsMax) })
		case "io":
			line("blockRead", "bytes/s", func(s store.WorkerSample) any { return orNil(s.BlkReadBps) })
			line("blockWrite", "bytes/s", func(s store.WorkerSample) any { return orNil(s.BlkWriteBps) })
		case "health":
			line("health", "health", func(s store.WorkerSample) any { return s.HealthWorst })
		case "jobs":
			jobs, err := f.jobSeries(ctx, id, g)
			if err != nil {
				return Timeseries{}, err
			}
			out.Series = append(out.Series, jobs...)
		}
	}
	return out, nil
}

// HostTimeseries returns the history of a host.
func (f *Fleet) HostTimeseries(ctx context.Context, name string, series []string, rng string) (Timeseries, error) {
	spec, err := rangeOf(rng)
	if err != nil {
		return Timeseries{}, err
	}
	if err := checkSeries(series, HostSeriesNames); err != nil {
		return Timeseries{}, err
	}
	f.mu.Lock()
	h, ok := f.hosts[name]
	f.mu.Unlock()
	if !ok {
		return Timeseries{}, errorf(http.StatusNotFound, "NOT_FOUND", "no host %s", name)
	}
	now := f.now()
	since := now.Add(-spec.d)
	buffer, err := f.fromBuffer(ctx, spec, since, func(ctx context.Context, from, to int64) (bool, error) {
		rows, err := f.store.HostSamples(ctx, name, from, to)
		return len(rows) > 0, err
	})
	if err != nil {
		return Timeseries{}, err
	}
	if !buffer {
		return f.hostSamples(ctx, name, series, newGrid(since, now, max(spec.bucket, 60)))
	}
	f.mu.Lock()
	var points []HostPoint
	for _, p := range h.ring.Points() {
		if !p.T.Before(since) {
			points = append(points, p)
		}
	}
	f.mu.Unlock()

	out := Timeseries{Source: "buffer", BucketSeconds: f.cfg.Polling.IntervalSeconds, Series: []Series{}}
	line := func(name, unit string, value func(HostPoint) any) {
		s := Series{Name: name, Unit: unit, Points: [][]any{}}
		for _, p := range points {
			v := any(nil)
			// a poll that failed is a gap, drawn as a break
			if p.Reachable {
				v = value(p)
			}
			s.Points = append(s.Points, []any{p.T.Unix(), v})
		}
		out.Series = append(out.Series, s)
	}
	paths := map[string]bool{}
	for _, p := range points {
		for _, d := range p.Disks {
			paths[d.Path] = true
		}
	}
	for _, name := range series {
		switch name {
		case "cpu":
			line("cpu", "ratio", func(p HostPoint) any { return p.CPURatio })
		case "memory":
			line("memoryUsed", "bytes", func(p HostPoint) any { return p.MemUsed })
			line("memoryTotal", "bytes", func(p HostPoint) any { return p.MemTotal })
		case "load":
			line("load1", "load", func(p HostPoint) any { return p.Load1 })
		case "disk":
			for _, path := range sortedKeys(paths) {
				line("disk:"+path, "ratio", func(p HostPoint) any {
					for _, d := range p.Disks {
						if d.Path == path {
							return d.UsedRatio
						}
					}
					return nil
				})
			}
		case "net":
			line("netRx", "bytes/s", func(p HostPoint) any { return p.NetRx })
			line("netTx", "bytes/s", func(p HostPoint) any { return p.NetTx })
		}
	}
	return out, nil
}

// hostSamples serves a host's chart from its minute samples; a bucket where the host never answered is a gap.
func (f *Fleet) hostSamples(ctx context.Context, name string, series []string, g grid) (Timeseries, error) {
	rows, err := f.store.HostSamples(ctx, name, g.from, g.to+int64(g.bucket)-1)
	if err != nil {
		return Timeseries{}, errorf(http.StatusInternalServerError, "INTERNAL", "failed to read the history: %v", err)
	}
	buckets := map[int64]store.HostSample{}
	paths := map[string]bool{}
	for _, r := range rows {
		for _, d := range r.Disks {
			paths[d.Path] = true
		}
		b := g.of(r.Minute)
		if old, ok := buckets[b]; ok {
			r = store.MergeHostSamples(old, r)
		}
		buckets[b] = r
	}

	out := Timeseries{Source: "samples", BucketSeconds: g.bucket, Series: []Series{}}
	line := func(name, unit string, value func(store.HostSample) any) {
		out.Series = append(out.Series, g.series(name, unit, func(b int64) any {
			s, ok := buckets[b]
			if !ok || s.ReachablePolls == 0 {
				return nil
			}
			return value(s)
		}))
	}
	for _, name := range series {
		switch name {
		case "cpu":
			line("cpu", "ratio", func(s store.HostSample) any { return orNil(s.CPUAvg) })
			line("cpuMax", "ratio", func(s store.HostSample) any { return orNil(s.CPUMax) })
		case "memory":
			line("memoryUsed", "bytes", func(s store.HostSample) any { return orNil(s.MemUsedAvg) })
			line("memoryTotal", "bytes", func(s store.HostSample) any { return orNil(s.MemTotal) })
		case "load":
			line("load1", "load", func(s store.HostSample) any { return orNil(s.Load1Avg) })
		case "disk":
			for _, path := range sortedKeys(paths) {
				line("disk:"+path, "ratio", func(s store.HostSample) any {
					for _, d := range s.Disks {
						if d.Path == path {
							return d.UsedRatioMax
						}
					}
					return nil
				})
			}
		case "net":
			line("netRx", "bytes/s", func(s store.HostSample) any { return orNil(s.NetRxBps) })
			line("netTx", "bytes/s", func(s store.HostSample) any { return orNil(s.NetTxBps) })
		}
	}
	return out, nil
}

func orNil[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
