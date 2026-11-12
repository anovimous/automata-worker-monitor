// Package discovery keeps the list of the worker containers of this host. Docker is the only truth: the list is
// rebuilt from a full refresh (every 10 s by default) and kept fresh in between by the Docker events, so that the
// worker machine controller needs no state of its own.
package discovery

import (
	"context"
	"errors"
	log "log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// the events stream is opened again after a break, waiting longer each time
const (
	minEventsBackoff = time.Second
	maxEventsBackoff = 30 * time.Second
)

// Container is a worker container as the controller sees it.
type Container struct {
	docker.ContainerInfo
	WorkerID string
	Status   string // Docker's text, from the last full refresh
	// OOMEvents counts the oom events seen since the controller started
	OOMEvents int
	// TelemetryURL is where GET /status of the worker is; "" without a telemetry port label
	TelemetryURL string
	// TelemetryConfigured tells whether the container has a telemetry port label
	TelemetryConfigured bool
	// Duplicate is set when another container carries the same worker id
	Duplicate bool
}

type Discovery struct {
	engine docker.Engine

	mu         sync.RWMutex
	containers map[string]*Container // by container id
	oomEvents  map[string]int        // by container id
}

func New(engine docker.Engine) *Discovery {
	return &Discovery{engine: engine, containers: map[string]*Container{}, oomEvents: map[string]int{}}
}

// Refresh rebuilds the list from Docker: new containers and those whose state changed are inspected again, the gone
// ones are dropped.
func (d *Discovery) Refresh(ctx context.Context) error {
	list, err := d.engine.ListWorkerContainers(ctx)
	if err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, s := range list {
		seen[s.ID] = true
		d.mu.RLock()
		known, ok := d.containers[s.ID]
		changed := !ok || known.State != s.State || known.Status != s.Status
		d.mu.RUnlock()
		if !changed {
			continue
		}
		info, err := d.engine.Inspect(ctx, s.ID)
		if errors.Is(err, docker.ErrNotFound) {
			seen[s.ID] = false
			continue
		}
		if err != nil {
			return err
		}
		d.store(info, s.Status)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.containers {
		if !seen[id] {
			delete(d.containers, id)
			delete(d.oomEvents, id)
		}
	}
	return nil
}

// Update inspects one container again, after an event or an action on it; a container that is gone is dropped.
func (d *Discovery) Update(ctx context.Context, containerID string) {
	info, err := d.engine.Inspect(ctx, containerID)
	if errors.Is(err, docker.ErrNotFound) {
		d.drop(containerID)
		return
	}
	if err != nil {
		log.Warn("failed to inspect a worker container", "containerId", containerID, "error", err)
		return
	}
	d.mu.RLock()
	status := ""
	if known, ok := d.containers[containerID]; ok {
		status = known.Status
	}
	d.mu.RUnlock()
	d.store(info, status)
}

// Handle applies a Docker event: an oom is counted, a destroy drops the container, any other one inspects it again.
func (d *Discovery) Handle(ctx context.Context, ev docker.Event) {
	switch ev.Action {
	case "oom":
		d.mu.Lock()
		d.oomEvents[ev.ContainerID]++
		if c, ok := d.containers[ev.ContainerID]; ok {
			c.OOMEvents = d.oomEvents[ev.ContainerID]
		}
		d.mu.Unlock()
	case "destroy":
		d.drop(ev.ContainerID)
	default:
		d.Update(ctx, ev.ContainerID)
	}
}

func (d *Discovery) drop(containerID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.containers, containerID)
	delete(d.oomEvents, containerID)
}

func (d *Discovery) store(info docker.ContainerInfo, status string) {
	c := &Container{ContainerInfo: info, WorkerID: info.Labels[workerapi.LabelID], Status: status}
	c.TelemetryURL, c.TelemetryConfigured = telemetryURL(info)
	d.mu.Lock()
	defer d.mu.Unlock()
	c.OOMEvents = d.oomEvents[info.ID]
	d.containers[info.ID] = c
}

// telemetryURL is where the controller reaches GET /status: the loopback in host mode (the controller runs with
// host networking too), the container's address on its network otherwise.
func telemetryURL(info docker.ContainerInfo) (url string, configured bool) {
	port := info.Labels[workerapi.LabelTelemetryPort]
	if port == "" {
		return "", false
	}
	host := info.IP
	if info.Labels[workerapi.LabelNetworkMode] == "host" || info.NetworkMode == "host" {
		host = "127.0.0.1"
	}
	if host == "" {
		// a bridge container that is not running has no address
		return "", true
	}
	return "http://" + net.JoinHostPort(host, port), true
}

// Workers returns the worker containers ordered by worker id, the containers sharing a worker id flagged.
func (d *Discovery) Workers() []Container {
	d.mu.RLock()
	out := make([]Container, 0, len(d.containers))
	count := map[string]int{}
	for _, c := range d.containers {
		out = append(out, *c)
		count[c.WorkerID]++
	}
	d.mu.RUnlock()

	for i := range out {
		out[i].Duplicate = count[out[i].WorkerID] > 1
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WorkerID != out[j].WorkerID {
			return out[i].WorkerID < out[j].WorkerID
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Find returns the container of a worker. With duplicates it is the one created first, and ok tells whether there
// is one at all.
func (d *Discovery) Find(workerID string) (c Container, ok bool) {
	for _, w := range d.Workers() {
		if w.WorkerID != workerID {
			continue
		}
		if !ok || w.Created.Before(c.Created) {
			c, ok = w, true
		}
	}
	return c, ok
}

// Run keeps the list fresh until ctx is done: a full refresh every interval, the events in between.
func (d *Discovery) Run(ctx context.Context, interval time.Duration) {
	if err := d.Refresh(ctx); err != nil {
		log.Warn("failed to list the worker containers", "error", err)
	}

	go d.followEvents(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.Refresh(ctx); err != nil && ctx.Err() == nil {
				log.Warn("failed to list the worker containers", "error", err)
			}
		}
	}
}

// followEvents applies the Docker events; a broken stream is opened again after a backoff, and a refresh fills what
// was missed meanwhile.
func (d *Discovery) followEvents(ctx context.Context) {
	backoff := minEventsBackoff
	for ctx.Err() == nil {
		evs, errs := d.engine.Events(ctx)
		opened := time.Now()
	read:
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-evs:
				if !ok {
					break read
				}
				d.Handle(ctx, ev)
			case err := <-errs:
				if ctx.Err() == nil {
					log.Warn("the Docker events stream broke", "error", err)
				}
				break read
			}
		}
		if time.Since(opened) > maxEventsBackoff {
			backoff = minEventsBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxEventsBackoff)
		if err := d.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.Warn("failed to list the worker containers", "error", err)
		}
	}
}
