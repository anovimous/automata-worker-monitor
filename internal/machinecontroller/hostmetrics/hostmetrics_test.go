package hostmetrics

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// fakeSource answers fixed values that the test changes.
type fakeSource struct {
	nets    map[string]Counters
	failing map[string]bool
}

func (f *fakeSource) err(part string) error {
	if f.failing[part] {
		return errors.New(part + " unreadable")
	}
	return nil
}

func (f *fakeSource) CPUUsageRatio() (float64, error) { return 0.41, f.err("cpu") }
func (f *fakeSource) NumCPU() (int, error)            { return 8, f.err("cpus") }
func (f *fakeSource) Load() (float64, float64, float64, error) {
	return 2.1, 1.8, 1.5, f.err("load")
}
func (f *fakeSource) Memory() (uint64, uint64, error) {
	return 33554432000, 20132659200, f.err("memory")
}
func (f *fakeSource) SwapUsed() (uint64, error) { return 0, f.err("swap") }
func (f *fakeSource) Disk(path string) (uint64, uint64, error) {
	if f.failing["disk "+path] {
		return 0, 0, errors.New("no such path")
	}
	return 500107862016, 312567889920, nil
}
func (f *fakeSource) Networks() (map[string]Counters, error) { return f.nets, f.err("network") }
func (f *fakeSource) Uptime() (uint64, error)                { return 864000, f.err("uptime") }
func (f *fakeSource) Kernel() (string, error)                { return "7.0.0-38-generic", f.err("kernel") }

var t0 = time.Date(2026, 10, 29, 10, 42, 12, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestSampleReadsEveryPart(t *testing.T) {
	src := &fakeSource{nets: map[string]Counters{"eth0": {Rx: 100, Tx: 50}, "lo": {Rx: 9, Tx: 9}, "veth12ab": {Rx: 7, Tx: 7}, "docker0": {Rx: 1, Tx: 1}}}
	s := New(src, []string{"/host/var/lib/docker"}, "29.6.1", (&clock{t0}).now)

	m := s.Sample()
	if m.CPUs != 8 || m.CPUUsageRatio != 0.41 || m.Load1 != 2.1 || m.Load5 != 1.8 || m.Load15 != 1.5 {
		t.Errorf("cpu and load = %+v", m)
	}
	if m.MemoryTotalBytes != 33554432000 || m.MemoryAvailableBytes != 20132659200 || m.SwapUsedBytes != 0 {
		t.Errorf("memory = %+v", m)
	}
	if len(m.Disks) != 1 || m.Disks[0].Path != "/host/var/lib/docker" || m.Disks[0].FreeBytes != 312567889920 {
		t.Errorf("disks = %+v", m.Disks)
	}
	if m.UptimeSeconds != 864000 || m.Kernel != "7.0.0-38-generic" || m.DockerVersion != "29.6.1" || !m.SampledAt.Equal(t0) {
		t.Errorf("host = %+v", m)
	}
	if len(m.Networks) != 2 || m.Networks[0].Interface != "docker0" || m.Networks[1].Interface != "eth0" {
		t.Errorf("networks = %+v, want docker0 and eth0, without lo and veth", m.Networks)
	}
	if m.Networks[1].RxBytesPerSec != 0 {
		t.Error("the first sample has a rate")
	}
	if m.VolumesBytes != nil {
		t.Error("volumesBytes is set here, it comes from the volume sampler")
	}
	if s.Latest().CPUs != 8 {
		t.Error("Latest is not the last sample")
	}
}

func TestNetworkRates(t *testing.T) {
	c := &clock{t0}
	src := &fakeSource{nets: map[string]Counters{"eth0": {Rx: 1000, Tx: 500}}}
	s := New(src, nil, "", c.now)
	s.Sample()

	c.t = t0.Add(5 * time.Second)
	src.nets = map[string]Counters{"eth0": {Rx: 6550000 + 1000, Tx: 510000 + 500}}
	m := s.Sample()
	if ni := m.Networks[0]; ni.RxBytesPerSec != 1310000 || ni.TxBytesPerSec != 102000 {
		t.Errorf("rates = %+v, want 1310000 and 102000 B/s", ni)
	}

	c.t = t0.Add(10 * time.Second)
	src.nets = map[string]Counters{"eth0": {Rx: 10, Tx: 10}}
	if ni := s.Sample().Networks[0]; ni.RxBytesPerSec != 0 || ni.TxBytesPerSec != 0 {
		t.Errorf("rates after a counter reset = %+v, want 0", ni)
	}
}

func TestAPartThatFailsIsLeftOut(t *testing.T) {
	src := &fakeSource{failing: map[string]bool{"load": true, "disk /missing": true, "network": true}}
	s := New(src, []string{"/missing", "/host/var/lib/docker"}, "", (&clock{t0}).now)

	m := s.Sample()
	if m.Load1 != 0 || len(m.Disks) != 1 || m.Disks[0].Path != "/host/var/lib/docker" || len(m.Networks) != 0 {
		t.Errorf("sample = %+v, want load, the missing disk and the network left out", m)
	}
	if m.CPUs != 8 || m.MemoryTotalBytes == 0 {
		t.Error("the parts that work were left out too")
	}
	if m.Disks == nil || m.Networks == nil {
		t.Error("an empty list is nil, it must be written as []")
	}
}

func TestRunSamplesUntilItsContextEnds(t *testing.T) {
	s := New(&fakeSource{}, nil, "", time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	if s.Latest().CPUs != 8 {
		t.Error("Run did not sample")
	}
}

// the real source on this machine: every part answers something sane
func TestGopsutilReadsThisHost(t *testing.T) {
	g := Gopsutil{}
	if n, err := g.NumCPU(); err != nil || n < 1 {
		t.Errorf("NumCPU = %d, %v", n, err)
	}
	if total, available, err := g.Memory(); err != nil || total == 0 || available > total {
		t.Errorf("Memory = %d, %d, %v", total, available, err)
	}
	dir, _ := os.Getwd()
	if total, free, err := g.Disk(dir); err != nil || total == 0 || free > total {
		t.Errorf("Disk = %d, %d, %v", total, free, err)
	}
	if nets, err := g.Networks(); err != nil || len(nets) == 0 {
		t.Errorf("Networks = %v, %v", nets, err)
	}
	if v, err := g.CPUUsageRatio(); err != nil || v < 0 || v > 1 {
		t.Errorf("CPUUsageRatio = %v, %v", v, err)
	}
	for name, fn := range map[string]func() error{
		"load":   func() error { _, _, _, err := g.Load(); return err },
		"swap":   func() error { _, err := g.SwapUsed(); return err },
		"uptime": func() error { _, err := g.Uptime(); return err },
		"kernel": func() error { _, err := g.Kernel(); return err },
	} {
		if err := fn(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
