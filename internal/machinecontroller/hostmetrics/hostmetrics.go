// Package hostmetrics samples the host the workers run on (CPU, load, memory, disks, network). In a container it
// reads the host's /proc and /sys through HOST_PROC, HOST_SYS and HOST_ROOT, which gopsutil follows.
package hostmetrics

import (
	"context"
	log "log/slog"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
)

// Counters are the byte counters of a network interface.
type Counters struct {
	Rx, Tx uint64
}

// Source is where the samples come from; Gopsutil reads the host.
type Source interface {
	CPUUsageRatio() (float64, error) // since the previous call
	NumCPU() (int, error)
	Load() (l1, l5, l15 float64, err error)
	Memory() (total, available uint64, err error)
	SwapUsed() (uint64, error)
	Disk(path string) (total, free uint64, err error)
	Networks() (map[string]Counters, error)
	Uptime() (uint64, error)
	Kernel() (string, error)
}

// Sampler keeps the latest sample of the host.
type Sampler struct {
	src           Source
	diskPaths     []string
	dockerVersion string
	now           func() time.Time

	mu      sync.Mutex
	latest  controllerapi.HostMetrics
	prevNet map[string]Counters
	prevAt  time.Time
	warned  map[string]bool
}

func New(src Source, diskPaths []string, dockerVersion string, now func() time.Time) *Sampler {
	return &Sampler{src: src, diskPaths: diskPaths, dockerVersion: dockerVersion, now: now, warned: map[string]bool{}}
}

// Sample reads the host now and keeps the result. A part that can not be read is left empty, and logged once.
func (s *Sampler) Sample() controllerapi.HostMetrics {
	now := s.now().UTC()
	m := controllerapi.HostMetrics{SampledAt: now, DockerVersion: s.dockerVersion, Disks: []controllerapi.Disk{}, Networks: []controllerapi.NetworkInterface{}}

	if v, err := s.src.CPUUsageRatio(); s.ok("cpu", err) {
		m.CPUUsageRatio = v
	}
	if v, err := s.src.NumCPU(); s.ok("cpus", err) {
		m.CPUs = v
	}
	if l1, l5, l15, err := s.src.Load(); s.ok("load", err) {
		m.Load1, m.Load5, m.Load15 = l1, l5, l15
	}
	if total, available, err := s.src.Memory(); s.ok("memory", err) {
		m.MemoryTotalBytes, m.MemoryAvailableBytes = int64(total), int64(available)
	}
	if v, err := s.src.SwapUsed(); s.ok("swap", err) {
		m.SwapUsedBytes = int64(v)
	}
	for _, p := range s.diskPaths {
		if total, free, err := s.src.Disk(p); s.ok("disk "+p, err) {
			m.Disks = append(m.Disks, controllerapi.Disk{Path: p, TotalBytes: int64(total), FreeBytes: int64(free)})
		}
	}
	if v, err := s.src.Uptime(); s.ok("uptime", err) {
		m.UptimeSeconds = int64(v)
	}
	if v, err := s.src.Kernel(); s.ok("kernel", err) {
		m.Kernel = v
	}

	nets, err := s.src.Networks()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.okLocked("network", err) {
		m.Networks = s.rates(nets, now)
	}
	s.latest = m
	return m
}

// rates turns the counters into interfaces with their rates since the previous sample; the loopback and the veth
// ends of the containers are left out (the containers' traffic is counted on the host's own interfaces).
func (s *Sampler) rates(nets map[string]Counters, now time.Time) []controllerapi.NetworkInterface {
	elapsed := now.Sub(s.prevAt).Seconds()
	out := []controllerapi.NetworkInterface{}
	for name, c := range nets {
		if name == "lo" || strings.HasPrefix(name, "veth") {
			continue
		}
		ni := controllerapi.NetworkInterface{Interface: name, RxBytesTotal: int64(c.Rx), TxBytesTotal: int64(c.Tx)}
		// a counter going down (interface reset) gives no rate
		if prev, ok := s.prevNet[name]; ok && elapsed > 0 && c.Rx >= prev.Rx && c.Tx >= prev.Tx {
			ni.RxBytesPerSec = float64(c.Rx-prev.Rx) / elapsed
			ni.TxBytesPerSec = float64(c.Tx-prev.Tx) / elapsed
		}
		out = append(out, ni)
	}
	sortInterfaces(out)
	s.prevNet, s.prevAt = nets, now
	return out
}

func sortInterfaces(nis []controllerapi.NetworkInterface) {
	for i := 1; i < len(nis); i++ {
		for j := i; j > 0 && nis[j].Interface < nis[j-1].Interface; j-- {
			nis[j], nis[j-1] = nis[j-1], nis[j]
		}
	}
}

func (s *Sampler) ok(part string, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.okLocked(part, err)
}

func (s *Sampler) okLocked(part string, err error) bool {
	if err == nil {
		return true
	}
	if !s.warned[part] {
		s.warned[part] = true
		log.Warn("failed to read a host metric, it is left out", "metric", part, "error", err)
	}
	return false
}

// Latest is the last sample.
func (s *Sampler) Latest() controllerapi.HostMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// Run samples every interval until ctx is done.
func (s *Sampler) Run(ctx context.Context, interval time.Duration) {
	s.Sample()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sample()
		}
	}
}

// Gopsutil is the Source of the real host.
type Gopsutil struct{}

func (Gopsutil) CPUUsageRatio() (float64, error) {
	p, err := cpu.Percent(0, false)
	if err != nil || len(p) == 0 {
		return 0, err
	}
	return p[0] / 100, nil
}

func (Gopsutil) NumCPU() (int, error) { return cpu.Counts(true) }

func (Gopsutil) Load() (float64, float64, float64, error) {
	a, err := load.Avg()
	if err != nil {
		return 0, 0, 0, err
	}
	return a.Load1, a.Load5, a.Load15, nil
}

func (Gopsutil) Memory() (uint64, uint64, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0, 0, err
	}
	return v.Total, v.Available, nil
}

func (Gopsutil) SwapUsed() (uint64, error) {
	s, err := mem.SwapMemory()
	if err != nil {
		return 0, err
	}
	return s.Used, nil
}

func (Gopsutil) Disk(path string) (uint64, uint64, error) {
	u, err := disk.Usage(path)
	if err != nil {
		return 0, 0, err
	}
	return u.Total, u.Free, nil
}

func (Gopsutil) Networks() (map[string]Counters, error) {
	stats, err := psnet.IOCounters(true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Counters, len(stats))
	for _, s := range stats {
		out[s.Name] = Counters{Rx: s.BytesRecv, Tx: s.BytesSent}
	}
	return out, nil
}

func (Gopsutil) Uptime() (uint64, error) { return host.Uptime() }

func (Gopsutil) Kernel() (string, error) { return host.KernelVersion() }
