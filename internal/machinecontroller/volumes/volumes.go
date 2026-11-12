// Package volumes reports the size of the worker volumes: the wordlist cache and the work dir of each worker, and
// their total for the host. Docker computes the sizes by walking the volumes, so they are read every 60 s only.
package volumes

import (
	"context"
	log "log/slog"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// Interval is how often the sizes are read.
const Interval = 60 * time.Second

// where each worker volume is mounted, by its name prefix (provision.VolumeNames)
var mountPaths = []struct{ prefix, path string }{
	{"automata-wl-", "/var/cache/automata/wordlists"},
	{"automata-tmp-", "/var/lib/automata/tmp"},
}

type Sampler struct {
	engine docker.Engine
	now    func() time.Time

	mu       sync.Mutex
	byWorker map[string]controllerapi.Volumes
	total    *int64
}

func New(engine docker.Engine, now func() time.Time) *Sampler {
	return &Sampler{engine: engine, now: now, byWorker: map[string]controllerapi.Volumes{}}
}

// Sample reads the sizes now.
func (s *Sampler) Sample(ctx context.Context) error {
	usage, err := s.engine.VolumeUsage(ctx)
	if err != nil {
		return err
	}
	at := s.now().UTC()
	byWorker := map[string]controllerapi.Volumes{}
	var total int64
	for _, prefix := range mountPaths {
		for _, v := range usage {
			workerID := v.Labels[workerapi.LabelID]
			if workerID == "" || v.Name != prefix.prefix+workerID {
				continue
			}
			item := controllerapi.Volume{Name: v.Name, MountPath: prefix.path}
			if v.Size >= 0 {
				size := v.Size
				item.Bytes = &size
				total += size
			}
			vols := byWorker[workerID]
			vols.SampledAt = at
			vols.Items = append(vols.Items, item)
			byWorker[workerID] = vols
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.byWorker, s.total = byWorker, &total
	return nil
}

// Get returns the volumes of a worker; nil before the first sample or for a worker without volumes.
func (s *Sampler) Get(workerID string) *controllerapi.Volumes {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byWorker[workerID]
	if !ok {
		return nil
	}
	v.Items = append([]controllerapi.Volume(nil), v.Items...)
	return &v
}

// Total is the size of all the worker volumes of the host; nil before the first sample.
func (s *Sampler) Total() *int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total == nil {
		return nil
	}
	t := *s.total
	return &t
}

// Run samples every interval until ctx is done.
func (s *Sampler) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := s.Sample(ctx); err != nil && ctx.Err() == nil {
			log.Warn("failed to read the size of the worker volumes", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
