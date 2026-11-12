package stats

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
)

var t0 = time.Date(2026, 10, 29, 10, 42, 13, 0, time.UTC)

// the sample of the snapshot sample's running worker
func sample(read time.Time, cpuNs uint64) docker.Sample {
	return docker.Sample{Read: read, CPUTotalNs: cpuNs, CPUThrottledNs: 12400000000, MemUsage: 1003020288,
		MemInactiveFile: 190373888, MemLimit: 4294967296, Pids: 9, PidsLimit: 512, BlockRead: 10485760, BlockWrite: 73400320}
}

func TestComputeTheFirstSample(t *testing.T) {
	r := Compute(nil, sample(t0, 4812200000000))

	if r.CPUCores != 0 || r.CPUUsageSecondsTotal != 4812.2 || r.CPUThrottledSecondsTotal != 12.4 {
		t.Errorf("cpu = %v cores, %v s, %v s throttled; want no rate on the first sample", r.CPUCores, r.CPUUsageSecondsTotal, r.CPUThrottledSecondsTotal)
	}
	if r.MemoryWorkingSetBytes != 812646400 || r.MemoryUsageBytes != 1003020288 || r.MemoryLimitBytes != 4294967296 {
		t.Errorf("memory = %+v", r)
	}
	if r.Pids != 9 || r.PidsLimit == nil || *r.PidsLimit != 512 || r.BlockReadBytesTotal != 10485760 || r.BlockWriteBytesTotal != 73400320 {
		t.Errorf("pids and io = %+v", r)
	}
	if !r.SampledAt.Equal(t0) {
		t.Errorf("sampledAt = %s", r.SampledAt)
	}
	if r.NetRxBytesTotal != nil || r.NetTxBytesTotal != nil || r.NetRxBytesPerSec != nil || r.NetTxBytesPerSec != nil {
		t.Error("network fields are set, want null (R3 is delayed)")
	}
}

func TestComputeTheCPUCores(t *testing.T) {
	prev := sample(t0, 4800000000000)
	cur := sample(t0.Add(5*time.Second), 4808650000000) // 8.65 s of CPU in 5 s

	if r := Compute(&prev, cur); math.Abs(r.CPUCores-1.73) > 1e-9 {
		t.Errorf("cpuCores = %v, want 1.73", r.CPUCores)
	}
}

func TestComputeGivesNoRateForACounterReset(t *testing.T) {
	prev := sample(t0, 4800000000000)
	if r := Compute(&prev, sample(t0.Add(5*time.Second), 1000)); r.CPUCores != 0 {
		t.Errorf("cpuCores after a restart = %v, want 0", r.CPUCores)
	}
	if r := Compute(&prev, sample(t0, 4900000000000)); r.CPUCores != 0 {
		t.Errorf("cpuCores at the same read time = %v, want 0", r.CPUCores)
	}
}

func TestComputeWithoutLimits(t *testing.T) {
	s := docker.Sample{Read: t0, MemUsage: 100, MemInactiveFile: 300, PidsLimit: math.MaxUint64}
	r := Compute(nil, s)
	if r.MemoryWorkingSetBytes != 0 || r.PidsLimit != nil {
		t.Errorf("working set %d pids limit %v, want 0 and null", r.MemoryWorkingSetBytes, r.PidsLimit)
	}
	if r := Compute(nil, docker.Sample{Read: t0}); r.PidsLimit != nil {
		t.Errorf("pids limit 0 = %v, want null", r.PidsLimit)
	}
}

func startedContainer(t *testing.T, f *docker.Fake, name string) string {
	t.Helper()
	ctx := context.Background()
	f.AddImage("automata-worker:0.4.0")
	id, err := f.Create(ctx, docker.CreateSpec{Name: name, Image: "automata-worker:0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	f.Start(ctx, id)
	return id
}

// eventually waits until cond holds.
func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// send feeds a sample once the stream is open.
func send(t *testing.T, f *docker.Fake, id string, s docker.Sample) {
	t.Helper()
	eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		return f.SendSample(ctx, id, s) == nil
	}, "the stats stream to read a sample")
}

func TestManagerKeepsTheLatestResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := docker.NewFake()
	id := startedContainer(t, f, "w")
	m := NewManager(f)
	defer m.Stop()

	if _, ok := m.Get(id); ok {
		t.Fatal("resources before any sample")
	}
	m.Sync(ctx, []string{id})
	send(t, f, id, sample(t0, 4800000000000))
	send(t, f, id, sample(t0.Add(5*time.Second), 4808650000000))

	eventually(t, func() bool {
		r, ok := m.Get(id)
		return ok && math.Abs(r.CPUCores-1.73) < 1e-9
	}, "the second sample with its rate")
}

func TestSyncStopsTheStreamOfAStoppedContainer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := docker.NewFake()
	id := startedContainer(t, f, "w")
	m := NewManager(f)
	defer m.Stop()

	m.Sync(ctx, []string{id})
	send(t, f, id, sample(t0, 1))
	eventually(t, func() bool { _, ok := m.Get(id); return ok }, "the first sample")

	m.Sync(ctx, nil)
	if _, ok := m.Get(id); ok {
		t.Error("the resources of a container no longer running are still there")
	}
}

func TestAStreamThatFailsIsOpenedAgain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := docker.NewFake()
	id := startedContainer(t, f, "w")
	f.SetErr("Stats", errors.New("daemon busy"))
	m := NewManager(f)
	defer m.Stop()

	m.Sync(ctx, []string{id})
	time.Sleep(50 * time.Millisecond)
	f.SetErr("Stats", nil)
	// the retry comes after the first backoff (1 s)
	send(t, f, id, sample(t0, 1))
	eventually(t, func() bool { _, ok := m.Get(id); return ok }, "a sample after the retry")
}
