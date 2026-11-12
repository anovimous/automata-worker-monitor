package history

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
)

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func TestMinute(t *testing.T) {
	if Minute(t0.Add(59*time.Second)) != t0.Unix() || Minute(t0.Add(time.Minute)) != t0.Unix()+60 {
		t.Error("Minute does not floor to the minute")
	}
}

func TestAMinuteOfPolls(t *testing.T) {
	a := New()
	a.Rollover(t0)
	for i, cpu := range []float64{1, 2, 3} {
		var read *float64
		if i > 0 {
			read = new(float64(i * 100))
		}
		a.Worker("w-1", t0.Add(time.Duration(i*5)*time.Second), WorkerPoll{CPUCores: new(cpu), CPULimit: new(2.0),
			MemWorkingSet: new(int64(cpu * 100)), MemLimit: new(int64(1000)), Pids: new(int64(cpu)), BlkRead: read,
			Health: []string{"HEALTHY", "STUCK", "HEALTHY"}[i], Responsive: i != 1, Busy: i > 0, Phase: "RUNNING_JOB",
			JobID: new(int64(4211)), Routine: new("fuzz")})
	}
	// a poll without resources (the container stopped) counts as a poll, not in the averages
	a.Worker("w-1", t0.Add(15*time.Second), WorkerPoll{Health: "DOWN", Phase: ""})

	if ws, _ := a.Rollover(t0.Add(30 * time.Second)); ws != nil {
		t.Fatal("a rollover within the minute returned rows")
	}
	ws, hs := a.Rollover(t0.Add(time.Minute))
	if len(ws) != 1 || len(hs) != 0 {
		t.Fatalf("rows = %+v %+v", ws, hs)
	}
	s := ws[0]
	if s.Minute != t0.Unix() || s.Polls != 4 || s.ResourcePolls != 3 || s.RatePolls != 2 || s.ResponsivePolls != 2 || s.BusyPolls != 2 {
		t.Errorf("counts = %+v", s)
	}
	if *s.CPUAvg != 2 || *s.CPUMax != 3 || *s.CPULimit != 2 || *s.MemAvg != 200 || *s.MemMax != 300 || *s.PidsMax != 3 || *s.BlkReadBps != 150 {
		t.Errorf("values = cpu %v/%v mem %v/%v pids %v read %v", *s.CPUAvg, *s.CPUMax, *s.MemAvg, *s.MemMax, *s.PidsMax, *s.BlkReadBps)
	}
	if s.HealthWorst != "DOWN" || s.PhaseLast != "" || s.JobIDLast != nil {
		t.Errorf("health %s phase %q job %v, want the worst and the last poll's", s.HealthWorst, s.PhaseLast, s.JobIDLast)
	}

	// the next minute starts empty
	if ws, _ := a.Rollover(t0.Add(2 * time.Minute)); ws != nil {
		t.Errorf("an empty minute gave rows: %+v", ws)
	}
}

func TestHostPolls(t *testing.T) {
	a := New()
	a.Host("host1", t0, HostPoll{Reachable: true, CPURatio: new(0.2), MemUsed: new(int64(10)), MemTotal: new(int64(100)),
		Load1: new(1.0), Disks: []store.DiskMax{{Path: "/", UsedRatioMax: 0.5}}, NetRx: new(100.0)})
	a.Host("host1", t0.Add(5*time.Second), HostPoll{})
	a.Host("host1", t0.Add(10*time.Second), HostPoll{Reachable: true, CPURatio: new(0.6), MemUsed: new(int64(30)),
		Disks: []store.DiskMax{{Path: "/", UsedRatioMax: 0.6}}, NetRx: new(300.0)})

	_, hs := a.Drain()
	if len(hs) != 1 {
		t.Fatalf("hosts = %+v", hs)
	}
	h := hs[0]
	if h.Polls != 3 || h.ReachablePolls != 2 || math.Abs(*h.CPUAvg-0.4) > 1e-9 || *h.CPUMax != 0.6 || *h.MemUsedAvg != 20 ||
		*h.MemTotal != 100 || *h.NetRxBps != 200 || h.Disks[0].UsedRatioMax != 0.6 {
		t.Errorf("host minute = %+v", h)
	}
}

func TestPollsOfTwoMinutesAreNotMixed(t *testing.T) {
	a := New()
	a.Worker("w-1", t0, WorkerPoll{Health: "HEALTHY"})
	// a poll of the next minute before the rollover: it starts a new row instead of adding to the old minute
	a.Worker("w-1", t0.Add(time.Minute), WorkerPoll{Health: "DOWN"})
	ws, _ := a.Drain()
	if len(ws) != 1 || ws[0].Minute != t0.Unix()+60 || ws[0].Polls != 1 {
		t.Errorf("rows = %+v", ws)
	}
}

func TestConcurrentUse(t *testing.T) {
	a := New()
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for j := range 100 {
				at := t0.Add(time.Duration(j) * time.Second)
				a.Worker("w", at, WorkerPoll{CPUCores: new(float64(i))})
				a.Host("h", at, HostPoll{Reachable: true})
				a.Rollover(at)
			}
		})
	}
	wg.Wait()
}
