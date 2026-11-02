package store

import (
	"context"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
func i64(v int64) *int64     { return &v }

func TestWorstHealth(t *testing.T) {
	order := []string{"NONE", "HEALTHY", "STARTING", "UNKNOWN", "DEGRADED", "STUCK", "UNRESPONSIVE", "DOWN"}
	for i := range order {
		for j := range order {
			want := order[max(i, j)]
			if got := WorstHealth(order[i], order[j]); got != want {
				t.Errorf("WorstHealth(%s, %s) = %s, want %s", order[i], order[j], got, want)
			}
		}
	}
}

func TestMergeWorkerSamples(t *testing.T) {
	a := WorkerSample{WorkerID: "w-1", Minute: 600, Polls: 4, ResourcePolls: 4, RatePolls: 3, CPUAvg: f64(1), CPUMax: f64(1.5),
		CPULimit: f64(2), MemAvg: i64(100), MemMax: i64(150), PidsMax: i64(9), BlkReadBps: f64(10), HealthWorst: "STUCK",
		ResponsivePolls: 1, BusyPolls: 4, PhaseLast: "RUNNING_JOB", JobIDLast: i64(7)}
	b := WorkerSample{WorkerID: "w-1", Minute: 600, Polls: 8, ResourcePolls: 4, RatePolls: 1, CPUAvg: f64(2), CPUMax: f64(1.8),
		MemAvg: i64(300), MemMax: i64(120), PidsMax: i64(4), BlkReadBps: f64(50), HealthWorst: "HEALTHY", ResponsivePolls: 8,
		BusyPolls: 0, PhaseLast: "IDLE"}

	m := MergeWorkerSamples(a, b)
	if m.Polls != 12 || m.ResourcePolls != 8 || m.RatePolls != 4 || m.ResponsivePolls != 9 || m.BusyPolls != 4 {
		t.Errorf("counts = %+v", m)
	}
	if *m.CPUAvg != 1.5 || *m.CPUMax != 1.8 || *m.CPULimit != 2 || *m.MemAvg != 200 || *m.MemMax != 150 || *m.PidsMax != 9 {
		t.Errorf("resources = cpu %v/%v limit %v mem %v/%v pids %v", *m.CPUAvg, *m.CPUMax, *m.CPULimit, *m.MemAvg, *m.MemMax, *m.PidsMax)
	}
	if *m.BlkReadBps != 20 || m.BlkWriteBps != nil {
		t.Errorf("rates = %v %v, want 20 weighted by rate polls and null", *m.BlkReadBps, m.BlkWriteBps)
	}
	if m.HealthWorst != "STUCK" || m.PhaseLast != "IDLE" || m.JobIDLast != nil {
		t.Errorf("health %s phase %s job %v, want the worst and the later part's last", m.HealthWorst, m.PhaseLast, m.JobIDLast)
	}
}

func TestMergeKeepsTheValuesOfThePartThatHasThem(t *testing.T) {
	a := WorkerSample{Polls: 3, ResourcePolls: 3, CPUAvg: f64(1)}
	b := WorkerSample{Polls: 2, HealthWorst: "DOWN"}
	if m := MergeWorkerSamples(a, b); *m.CPUAvg != 1 || m.HealthWorst != "DOWN" {
		t.Errorf("merge = %+v", m)
	}
}

func TestMergeHostSamples(t *testing.T) {
	a := HostSample{Host: "host1", Minute: 600, Polls: 6, ReachablePolls: 6, CPUAvg: f64(0.2), CPUMax: f64(0.5), MemUsedAvg: i64(10),
		MemTotal: i64(100), Load1Avg: f64(1), Disks: []DiskMax{{Path: "/a", UsedRatioMax: 0.4}}, NetRxBps: f64(100)}
	b := HostSample{Host: "host1", Minute: 600, Polls: 6, ReachablePolls: 2, CPUAvg: f64(0.6), CPUMax: f64(0.7), MemUsedAvg: i64(50),
		Load1Avg: f64(3), Disks: []DiskMax{{Path: "/a", UsedRatioMax: 0.3}, {Path: "/b", UsedRatioMax: 0.9}}, NetRxBps: f64(500)}
	m := MergeHostSamples(a, b)
	if m.Polls != 12 || m.ReachablePolls != 8 || !near(*m.CPUAvg, 0.3) || *m.CPUMax != 0.7 || *m.MemUsedAvg != 20 || *m.MemTotal != 100 ||
		!near(*m.Load1Avg, 1.5) || !near(*m.NetRxBps, 200) {
		t.Errorf("merge = %+v", m)
	}
	if len(m.Disks) != 2 || m.Disks[0].UsedRatioMax != 0.4 || m.Disks[1].Path != "/b" {
		t.Errorf("disks = %+v", m.Disks)
	}
}

func TestWriteAndReadSamples(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	w := WorkerSample{WorkerID: "w-1", Minute: 600, Polls: 6, ResourcePolls: 6, CPUAvg: f64(1), HealthWorst: "HEALTHY",
		ResponsivePolls: 6, BusyPolls: 3, PhaseLast: "RUNNING_JOB", JobIDLast: i64(4211), RoutineLast: new("fuzz")}
	h := HostSample{Host: "host1", Minute: 600, Polls: 6, ReachablePolls: 6, CPUAvg: f64(0.4), Disks: []DiskMax{{Path: "/", UsedRatioMax: 0.5}}}
	if err := s.WriteSamples(ctx, []WorkerSample{w}, []HostSample{h}); err != nil {
		t.Fatal(err)
	}

	// the control plane restarted within the minute: the rest of the minute adds to the row
	w2 := WorkerSample{WorkerID: "w-1", Minute: 600, Polls: 6, ResourcePolls: 6, CPUAvg: f64(3), HealthWorst: "DOWN", PhaseLast: "IDLE"}
	if err := s.WriteSamples(ctx, []WorkerSample{w2, {WorkerID: "w-1", Minute: 660, Polls: 12, HealthWorst: "HEALTHY"}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.WorkerSamples(ctx, "w-1", 0, 1000)
	if err != nil || len(got) != 2 {
		t.Fatalf("samples = %+v, %v", got, err)
	}
	if got[0].Polls != 12 || *got[0].CPUAvg != 2 || got[0].HealthWorst != "DOWN" || got[0].ResponsivePolls != 6 || got[0].PhaseLast != "IDLE" {
		t.Errorf("merged minute = %+v", got[0])
	}
	if only, _ := s.WorkerSamples(ctx, "w-1", 660, 660); len(only) != 1 || only[0].Minute != 660 {
		t.Errorf("one minute = %+v", only)
	}
	hs, err := s.HostSamples(ctx, "host1", 0, 1000)
	if err != nil || len(hs) != 1 || *hs[0].CPUAvg != 0.4 || hs[0].Disks[0].Path != "/" {
		t.Errorf("host samples = %+v, %v", hs, err)
	}
	if ok, _ := s.HasWorkerSamples(ctx, "w-1"); !ok {
		t.Error("HasWorkerSamples = false")
	}
	if ok, _ := s.HasWorkerSamples(ctx, "w-2"); ok {
		t.Error("HasWorkerSamples of a worker without minutes = true")
	}
}

func TestUsagesAndFleetMinutes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.WriteSamples(ctx, []WorkerSample{
		{WorkerID: "w-1", Minute: 600, Polls: 12, ResponsivePolls: 12, BusyPolls: 6, HealthWorst: "HEALTHY"},
		{WorkerID: "w-2", Minute: 600, Polls: 12, ResponsivePolls: 0, BusyPolls: 0, HealthWorst: "DOWN"},
		{WorkerID: "w-1", Minute: 660, Polls: 12, ResponsivePolls: 6, BusyPolls: 12, HealthWorst: "STUCK"},
	}, nil)

	u, err := s.Usages(ctx, 0)
	if err != nil || u["w-1"] != (Usage{Polls: 24, Responsive: 18, Busy: 18}) || u["w-2"] != (Usage{Polls: 12}) {
		t.Errorf("usages = %+v, %v", u, err)
	}
	if u, _ := s.Usages(ctx, 660); len(u) != 1 || u["w-1"].Polls != 12 {
		t.Errorf("usages since 660 = %+v", u)
	}
	fm, err := s.FleetMinutes(ctx, 0, 1000)
	if err != nil || len(fm) != 2 || fm[0] != (FleetMinute{Minute: 600, Active: 2, Busy: 0.5, Responsive: 1}) ||
		fm[1] != (FleetMinute{Minute: 660, Active: 1, Busy: 1, Responsive: 0.5}) {
		t.Errorf("fleet minutes = %+v, %v", fm, err)
	}
}

func TestJobsEnded(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.AddEvent(ctx, Event{WorkerID: "w-1", At: t0, Kind: EventJobEnded, To: "FINISHED"})
	s.AddEvent(ctx, Event{WorkerID: "w-2", At: t0.Add(time.Minute), Kind: EventJobEnded, To: "FAILED"})
	s.AddEvent(ctx, Event{WorkerID: "w-1", At: t0.Add(time.Minute), Kind: EventJobStarted, To: "4211"})

	all, _ := s.JobsEnded(ctx, "", t0, t0.Add(time.Hour))
	one, _ := s.JobsEnded(ctx, "w-1", t0, t0.Add(time.Hour))
	if len(all) != 2 || all[1].Outcome != "FAILED" || len(one) != 1 || one[0].Outcome != "FINISHED" || !one[0].At.Equal(t0) {
		t.Errorf("all %+v one %+v", all, one)
	}
}

func TestPurgeSamples(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.WriteSamples(ctx, []WorkerSample{{WorkerID: "w-1", Minute: 60, Polls: 1, HealthWorst: "HEALTHY"},
		{WorkerID: "w-1", Minute: 600, Polls: 1, HealthWorst: "HEALTHY"}},
		[]HostSample{{Host: "host1", Minute: 60, Polls: 1}})
	n, err := s.PurgeSamples(ctx, 600)
	if err != nil || n != 2 {
		t.Errorf("PurgeSamples = %d, %v; want 2", n, err)
	}
	if left, _ := s.WorkerSamples(ctx, "w-1", 0, 1000); len(left) != 1 || left[0].Minute != 600 {
		t.Errorf("left = %+v", left)
	}
}
