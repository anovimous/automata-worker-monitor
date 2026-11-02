package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
)

// samples returns the minute rows of a worker.
func (e *env) samples(id string) []store.WorkerSample {
	e.t.Helper()
	rows, err := e.store.WorkerSamples(context.Background(), id, 0, 1<<40)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

// pollUntil polls every 5 s until the clock reaches at.
func (e *env) pollUntil(id string, at time.Time) {
	for e.now().Before(at) {
		e.client.set(id, ready(e.later(5*time.Second)))
		e.poll(5 * time.Second)
	}
}

func TestMinuteSamplesAreWritten(t *testing.T) {
	e := newEnv(t)
	id := e.createActive() // polled at 10:00:05
	e.pollUntil(id, t0.Add(2*time.Minute))

	rows := e.samples(id)
	if len(rows) != 2 || rows[1].Polls != 12 {
		t.Fatalf("rows = %+v, want 10:00 and 10:01 (the poll of 10:02 rolled it over)", rows)
	}
	r := rows[0]
	if r.Minute != t0.Unix() || r.Polls != 11 || r.ResourcePolls != 11 || r.RatePolls != 10 || r.ResponsivePolls != 11 ||
		r.HealthWorst != "HEALTHY" || r.PhaseLast != "IDLE" || *r.CPUAvg != 0.5 || *r.MemMax != 100<<20 || *r.CPULimit != 2 {
		t.Errorf("minute = %+v", r)
	}
	hs, _ := e.store.HostSamples(context.Background(), "host1", 0, 1<<40)
	if len(hs) != 2 || hs[0].Polls != 11 || hs[0].ReachablePolls != 11 || hs[0].Disks[0].UsedRatioMax != 0.6 {
		t.Errorf("host minutes = %+v", hs)
	}

	// a stopped worker gets no rows: the minute of the stop still has the polls before it
	if _, err := e.f.Act(context.Background(), id, lifecycle.ActionStop, ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		e.poll(5 * time.Second)
	}
	for _, r := range e.samples(id) {
		if r.Minute > t0.Unix()+120 {
			t.Errorf("a stopped worker has a row at %d: %+v", r.Minute, r)
		}
	}
}

func TestAnUnreachableHostKeepsItsMinutes(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()
	e.client.setErr("snapshot", errors.New("boom"))
	for range 24 {
		e.poll(5 * time.Second)
	}
	hs, _ := e.store.HostSamples(context.Background(), "host1", t0.Unix()+60, t0.Unix()+60)
	if len(hs) != 1 || hs[0].Polls != 12 || hs[0].ReachablePolls != 0 || hs[0].CPUAvg != nil {
		t.Errorf("minute of an unreachable host = %+v", hs)
	}
	ws, _ := e.store.WorkerSamples(context.Background(), id, t0.Unix()+60, t0.Unix()+60)
	if len(ws) != 1 || ws[0].HealthWorst != "UNKNOWN" || ws[0].ResourcePolls != 0 || ws[0].BusyPolls != 0 {
		t.Errorf("minute of its worker = %+v", ws)
	}
}

func TestARestartWithinTheMinuteMerges(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()
	e.pollUntil(id, t0.Add(30*time.Second)) // 6 polls of 10:00
	e.f.FlushHistory(context.Background())

	e.f = e.fleet(e.store)
	e.pollUntil(id, t0.Add(65*time.Second)) // 5 more of 10:00, then 10:01 rolls it over
	rows := e.samples(id)
	if len(rows) != 1 || rows[0].Polls != 11 {
		t.Errorf("rows = %+v, want one minute of the 11 polls of both runs", rows)
	}
}

// writeMinutes writes minutes of a worker directly, from minute from (unix seconds) on.
func (e *env) writeMinutes(id string, from int64, n int, s store.WorkerSample) {
	e.t.Helper()
	var rows []store.WorkerSample
	for i := range n {
		s.WorkerID, s.Minute = id, from+int64(i*60)
		rows = append(rows, s)
	}
	if err := e.store.WriteSamples(context.Background(), rows, nil); err != nil {
		e.t.Fatal(err)
	}
}

func valueAt(s Series, t int64) any {
	for _, p := range s.Points {
		if p[0] == t {
			return p[1]
		}
	}
	return "missing"
}

func TestLongRanges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.clock = t0.Add(24 * time.Hour)
	day := t0.Unix()
	// 10:00–10:09 cpu 1, healthy; 10:10–10:19 nothing (stopped); 10:20–10:24 cpu 3 with a STUCK minute
	e.writeMinutes("w-1", day, 10, store.WorkerSample{Polls: 12, ResourcePolls: 12, CPUAvg: new(1.0), CPUMax: new(1.5),
		HealthWorst: "HEALTHY", ResponsivePolls: 12})
	e.writeMinutes("w-1", day+1200, 5, store.WorkerSample{Polls: 12, ResourcePolls: 12, CPUAvg: new(3.0), CPUMax: new(4.0),
		HealthWorst: "HEALTHY", ResponsivePolls: 12})
	e.writeMinutes("w-1", day+1260, 1, store.WorkerSample{Polls: 12, ResourcePolls: 12, CPUAvg: new(3.0), CPUMax: new(4.0),
		HealthWorst: "STUCK"})
	e.store.AddEvent(ctx, store.Event{WorkerID: "w-1", At: t0.Add(2 * time.Minute), Kind: store.EventJobEnded, To: "FINISHED"})
	e.store.AddEvent(ctx, store.Event{WorkerID: "w-1", At: t0.Add(3 * time.Minute), Kind: store.EventJobEnded, To: "FINISHED"})
	e.store.AddEvent(ctx, store.Event{WorkerID: "w-1", At: t0.Add(21 * time.Minute), Kind: store.EventJobEnded, To: "FAILED"})

	// the worker is not in the fleet: removed, its history stays
	ts, err := e.f.WorkerTimeseries(ctx, "w-1", []string{"cpu", "health", "jobs"}, "24h")
	if err != nil || ts.Source != "samples" || ts.BucketSeconds != 300 {
		t.Fatalf("24h = %+v, %v", ts, err)
	}
	cpu, cpuMax, health, failed, finished := ts.Series[0], ts.Series[1], ts.Series[3], ts.Series[4], ts.Series[5]
	if len(cpu.Points) != 289 || valueAt(cpu, day) != 1.0 || valueAt(cpu, day+600) != nil || valueAt(cpu, day+1200) != 3.0 ||
		valueAt(cpuMax, day+300) != 1.5 {
		t.Errorf("cpu = %d points, %v %v %v", len(cpu.Points), valueAt(cpu, day), valueAt(cpu, day+600), valueAt(cpu, day+1200))
	}
	if valueAt(health, day+1200) != "STUCK" || valueAt(health, day+600) != nil || valueAt(health, day) != "HEALTHY" {
		t.Errorf("health strip = %v", health.Points[:8])
	}
	if failed.Name != "jobs:FAILED" || finished.Name != "jobs:FINISHED" || valueAt(finished, day) != 2 ||
		valueAt(failed, day+1200) != 1 || valueAt(finished, day+600) != 0 {
		t.Errorf("jobs = %v %v", failed, finished)
	}

	six, _ := e.f.WorkerTimeseries(ctx, "w-1", []string{"cpu"}, "6h")
	if six.BucketSeconds != 60 || len(six.Series[0].Points) != 361 {
		t.Errorf("6h = %d s, %d points", six.BucketSeconds, len(six.Series[0].Points))
	}
	week, _ := e.f.WorkerTimeseries(ctx, "w-1", []string{"cpu"}, "7d")
	if week.BucketSeconds != 1800 || valueAt(week.Series[0], day) != 1.75 {
		t.Errorf("7d bucket of 10:00 = %v, want the average of its 16 minutes", valueAt(week.Series[0], day))
	}
	if _, err := e.f.WorkerTimeseries(ctx, "w-never", []string{"cpu"}, "24h"); err == nil {
		t.Error("a worker never seen has a history")
	}
}

func TestHostLongRange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.clock = t0.Add(6 * time.Hour)
	e.store.WriteSamples(ctx, nil, []store.HostSample{
		{Host: "host1", Minute: t0.Unix(), Polls: 12, ReachablePolls: 12, CPUAvg: new(0.5), Disks: []store.DiskMax{{Path: "/", UsedRatioMax: 0.7}}},
		{Host: "host1", Minute: t0.Unix() + 60, Polls: 12}})
	ts, err := e.f.HostTimeseries(ctx, "host1", []string{"cpu", "disk"}, "6h")
	if err != nil || ts.Source != "samples" || len(ts.Series) != 3 || ts.Series[2].Name != "disk:/" {
		t.Fatalf("6h = %+v, %v", ts, err)
	}
	if valueAt(ts.Series[0], t0.Unix()) != 0.5 || valueAt(ts.Series[0], t0.Unix()+60) != nil || valueAt(ts.Series[2], t0.Unix()) != 0.7 {
		t.Errorf("cpu = %v", ts.Series[0].Points[:3])
	}
}

func TestTheBufferFallsBackToSamplesAfterARestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.createActive()
	if ts, _ := e.f.WorkerTimeseries(ctx, id, []string{"cpu"}, "1h"); ts.Source != "buffer" {
		t.Errorf("a new control plane without history = %s, want buffer", ts.Source)
	}
	e.pollUntil(id, t0.Add(3*time.Minute))
	e.f.FlushHistory(ctx)

	e.f = e.fleet(e.store)
	e.pollUntil(id, t0.Add(4*time.Minute))
	ts, _ := e.f.WorkerTimeseries(ctx, id, []string{"cpu"}, "1h")
	if ts.Source != "samples" || ts.BucketSeconds != 60 || valueAt(ts.Series[0], t0.Unix()+60) != 0.5 {
		t.Errorf("1h after a restart = %+v", ts)
	}
	if hts, _ := e.f.HostTimeseries(ctx, "host1", []string{"cpu"}, "15m"); hts.Source != "samples" {
		t.Errorf("host 15m after a restart = %s", hts.Source)
	}
	// once the buffer covers the range, it serves it again
	e.clock = t0.Add(4*time.Minute + time.Hour)
	if ts, _ := e.f.WorkerTimeseries(ctx, id, []string{"cpu"}, "1h"); ts.Source != "buffer" {
		t.Errorf("1h an hour after the restart = %s", ts.Source)
	}
}

func TestAvailability(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.clock = t0.Add(time.Hour)
	e.writeMinutes("w-1", t0.Unix(), 10, store.WorkerSample{Polls: 12, ResponsivePolls: 9, BusyPolls: 6, HealthWorst: "STUCK"})
	e.writeMinutes("w-2", t0.Unix(), 10, store.WorkerSample{Polls: 12, ResponsivePolls: 12, HealthWorst: "HEALTHY"})
	e.writeMinutes("w-1", t0.Add(-8*24*time.Hour).Unix(), 1, store.WorkerSample{Polls: 12, HealthWorst: "DOWN"})

	a, err := e.f.WorkerAvailability(ctx, "w-1", "24h")
	if err != nil || a.Polls != 120 || *a.Availability != 0.75 || *a.Utilisation != 0.5 {
		t.Errorf("w-1 = %+v, %v", a, err)
	}
	all, err := e.f.FleetAvailability(ctx, "7d")
	if err != nil || len(all) != 2 || *all["w-2"].Availability != 1 || all["w-1"].Polls != 120 {
		t.Errorf("fleet = %+v, %v", all, err)
	}
	// a worker of the fleet without minutes yet: known, no figures
	id := e.createActive()
	if a, err := e.f.WorkerAvailability(ctx, id, "24h"); err != nil || a.Polls != 0 || a.Availability != nil {
		t.Errorf("new worker = %+v, %v", a, err)
	}
	var fe *Error
	if _, err := e.f.WorkerAvailability(ctx, "w-1", "1h"); !errors.As(err, &fe) || fe.Status != 400 {
		t.Errorf("range 1h = %v", err)
	}
	if _, err := e.f.WorkerAvailability(ctx, "w-never", "24h"); !errors.As(err, &fe) || fe.Status != 404 {
		t.Errorf("unknown worker = %v", err)
	}
}

func TestFleetTimeseries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.clock = t0.Add(time.Hour)
	e.writeMinutes("w-1", t0.Unix(), 5, store.WorkerSample{Polls: 12, ResponsivePolls: 12, BusyPolls: 12, HealthWorst: "HEALTHY"})
	e.writeMinutes("w-2", t0.Unix(), 5, store.WorkerSample{Polls: 12, ResponsivePolls: 6, HealthWorst: "STUCK"})
	e.store.AddEvent(ctx, store.Event{WorkerID: "w-1", At: t0.Add(time.Minute), Kind: store.EventJobEnded, To: "FINISHED"})

	ts, err := e.f.FleetTimeseries(ctx, []string{"capacity", "jobs"}, "6h")
	if err != nil || ts.BucketSeconds != 60 || len(ts.Series) != 4 {
		t.Fatalf("fleet = %+v, %v", ts, err)
	}
	active, busy, responsive, jobs := ts.Series[0], ts.Series[1], ts.Series[2], ts.Series[3]
	if valueAt(active, t0.Unix()) != 2.0 || valueAt(busy, t0.Unix()) != 1.0 || valueAt(responsive, t0.Unix()) != 1.5 ||
		valueAt(active, t0.Unix()+600) != nil || valueAt(jobs, t0.Unix()+60) != 1 {
		t.Errorf("capacity = %v %v %v jobs %v", active.Points[:2], busy.Points[:2], responsive.Points[:2], jobs.Points[:2])
	}
	// the short ranges come in minutes too: the fleet has no live buffer
	if short, _ := e.f.FleetTimeseries(ctx, []string{"capacity"}, "1h"); short.BucketSeconds != 60 || len(short.Series[0].Points) != 61 {
		t.Errorf("1h = %d s, %d points", short.BucketSeconds, len(short.Series[0].Points))
	}
	if _, err := e.f.FleetTimeseries(ctx, []string{"cpu"}, "6h"); err == nil {
		t.Error("an unknown fleet series was accepted")
	}
}

func TestPurgeOfTheHistory(t *testing.T) {
	e := newEnv(t)
	e.writeMinutes("w-1", t0.AddDate(0, 0, -20).Unix(), 1, store.WorkerSample{Polls: 1, HealthWorst: "HEALTHY"})
	e.writeMinutes("w-1", t0.AddDate(0, 0, -1).Unix(), 1, store.WorkerSample{Polls: 1, HealthWorst: "HEALTHY"})
	e.f.Purge(context.Background())
	if rows := e.samples("w-1"); len(rows) != 1 || rows[0].Minute != t0.AddDate(0, 0, -1).Unix() {
		t.Errorf("rows = %+v, want the one of yesterday (retention 14 days)", rows)
	}
}

func TestGrid(t *testing.T) {
	g := newGrid(time.Unix(1000, 0), time.Unix(1700, 0), 300)
	if g.from != 900 || g.to != 1500 || g.of(1499) != 1200 {
		t.Errorf("grid = %+v", g)
	}
	s := g.series("x", "u", func(b int64) any { return b })
	if len(s.Points) != 3 || s.Points[2][0] != int64(1500) {
		t.Errorf("points = %v", s.Points)
	}
}
