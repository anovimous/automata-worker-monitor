package fleet

import (
	"net/http"
	"slices"
	"time"
)

// Series is one line of a chart; a point is [unix seconds, value or null].
type Series struct {
	Name   string  `json:"name"`
	Unit   string  `json:"unit"`
	Points [][]any `json:"points"`
}

// Timeseries is the answer of the timeseries routes (plan/07 §8).
type Timeseries struct {
	Source        string   `json:"source"` // buffer (5 s points) or samples (minute samples, from P6)
	BucketSeconds int      `json:"bucketSeconds"`
	Series        []Series `json:"series"`
}

// the ranges served from the live buffer; the longer ones come with the minute samples (plan/09, P6)
var bufferRanges = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour}

// WorkerSeriesNames and HostSeriesNames are the series a chart may ask for.
var (
	WorkerSeriesNames = []string{"cpu", "memory", "pids", "io", "health"}
	HostSeriesNames   = []string{"cpu", "memory", "load", "disk", "net"}
)

func rangeOf(r string) (time.Duration, error) {
	d, ok := bufferRanges[r]
	if !ok {
		return 0, errorf(http.StatusBadRequest, "INVALID", "range must be 15m or 1h (the longer ranges come with the history)")
	}
	return d, nil
}

func checkSeries(asked, known []string) error {
	for _, s := range asked {
		if !slices.Contains(known, s) {
			return errorf(http.StatusBadRequest, "INVALID", "unknown series %q, the series are %v", s, known)
		}
	}
	return nil
}

// WorkerTimeseries returns the live history of a worker.
func (f *Fleet) WorkerTimeseries(id string, series []string, rng string) (Timeseries, error) {
	d, err := rangeOf(rng)
	if err != nil {
		return Timeseries{}, err
	}
	if err := checkSeries(series, WorkerSeriesNames); err != nil {
		return Timeseries{}, err
	}
	f.mu.Lock()
	w, ok := f.workers[id]
	f.mu.Unlock()
	if !ok {
		return Timeseries{}, errorf(http.StatusNotFound, "NOT_FOUND", "no worker %s", id)
	}
	since := f.now().Add(-d)
	var points []WorkerPoint
	for _, p := range w.ring.Points() {
		if !p.T.Before(since) {
			points = append(points, p)
		}
	}

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
		}
	}
	return out, nil
}

// HostTimeseries returns the live history of a host.
func (f *Fleet) HostTimeseries(name string, series []string, rng string) (Timeseries, error) {
	d, err := rangeOf(rng)
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
	since := f.now().Add(-d)
	var points []HostPoint
	for _, p := range h.ring.Points() {
		if !p.T.Before(since) {
			points = append(points, p)
		}
	}

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
