package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// Fake is an Engine in memory, for the tests of the other packages. Its containers behave like Docker's in the ways
// the controller relies on: a running container can not be removed, a stop exits with 0, a kill with 137.
type Fake struct {
	mu         sync.Mutex
	now        func() time.Time
	nextID     int
	containers map[string]*fakeContainer
	images     map[string]bool
	networks   map[string]map[string]string
	volumes    map[string]map[string]string
	volumeSize map[string]int64
	events     chan Event
	pulled     []string
	created    []CreateSpec
	errs       map[string]error
}

type fakeContainer struct {
	info   ContainerInfo
	status string
	logs   []byte
	stats  chan Sample
}

func NewFake() *Fake {
	return &Fake{
		now:        func() time.Time { return time.Now().UTC() },
		containers: map[string]*fakeContainer{},
		images:     map[string]bool{},
		networks:   map[string]map[string]string{"bridge": nil, "host": nil},
		volumes:    map[string]map[string]string{},
		volumeSize: map[string]int64{},
		events:     make(chan Event, 64),
		errs:       map[string]error{},
	}
}

// SetErr makes a method fail with err (nil makes it work again); method is the name, e.g. "Start".
func (f *Fake) SetErr(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, method)
		return
	}
	f.errs[method] = err
}

func (f *Fake) fail(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.errs[method]
}

// AddImage makes an image exist locally.
func (f *Fake) AddImage(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = true
}

// AddNetwork makes a network exist.
func (f *Fake) AddNetwork(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[name] = nil
}

// AddContainer adds a container as it is, e.g. one created before the controller started.
func (f *Fake) AddContainer(info ContainerInfo, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[info.ID] = &fakeContainer{info: info, status: status}
}

// SetContainer changes a container with fn, e.g. to make it exit.
func (f *Fake) SetContainer(id string, fn func(*ContainerInfo)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		fn(&c.info)
	}
}

// SetLogs gives a container its log lines.
func (f *Fake) SetLogs(id, stdout, stderr string) {
	var buf bytes.Buffer
	if stdout != "" {
		buf.Write(Frame(1, []byte(stdout)))
	}
	if stderr != "" {
		buf.Write(Frame(2, []byte(stderr)))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.logs = buf.Bytes()
	}
}

// Frame writes p as one frame of a multiplexed log stream, the way Docker does: an 8-byte header (stream 1 stdout,
// 2 stderr, three zero bytes, the length big-endian) then the bytes.
func Frame(stream byte, p []byte) []byte {
	header := make([]byte, 8, 8+len(p))
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(p)))
	return append(header, p...)
}

// SetVolumeSize sets the size VolumeUsage reports for a volume.
func (f *Fake) SetVolumeSize(name string, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumeSize[name] = size
}

// Emit sends a Docker event to the Events stream.
func (f *Fake) Emit(ev Event) {
	f.events <- ev
}

// SendSample feeds a stats sample to the stream of a container; it blocks until a stream reads it.
func (f *Fake) SendSample(ctx context.Context, id string, s Sample) error {
	f.mu.Lock()
	var stream chan Sample
	if c, ok := f.containers[id]; ok {
		stream = c.stats
	}
	f.mu.Unlock()
	if stream == nil {
		return fmt.Errorf("no stats stream for %s", id)
	}
	select {
	case stream <- s:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Created returns the specs given to Create.
func (f *Fake) Created() []CreateSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CreateSpec(nil), f.created...)
}

// Pulled returns the images pulled.
func (f *Fake) Pulled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pulled...)
}

// Volumes returns the names of the volumes that exist.
func (f *Fake) Volumes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CreateVolume makes a volume exist with labels, e.g. one left by an earlier worker.
func (f *Fake) CreateVolume(name string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes[name] = labels
}

func (f *Fake) Ping(ctx context.Context) (string, error) {
	return "1.55", f.fail("Ping")
}

func (f *Fake) Info(ctx context.Context) (Info, error) {
	return Info{ServerVersion: "29.6.1", APIVersion: "1.55", NCPU: 8, MemTotal: 32 << 30, KernelVersion: "7.0.0"}, f.fail("Info")
}

func (f *Fake) ListWorkerContainers(ctx context.Context) ([]ContainerSummary, error) {
	if err := f.fail("ListWorkerContainers"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerSummary
	for _, c := range f.containers {
		if c.info.Labels[workerapi.LabelManagedBy] != workerapi.ManagedByMonitor {
			continue
		}
		out = append(out, ContainerSummary{ID: c.info.ID, Name: c.info.Name, Labels: c.info.Labels, State: c.info.State, Status: c.status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *Fake) Inspect(ctx context.Context, id string) (ContainerInfo, error) {
	if err := f.fail("Inspect"); err != nil {
		return ContainerInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return ContainerInfo{}, fmt.Errorf("fake inspect %s: %w", id, ErrNotFound)
	}
	return c.info, nil
}

func (f *Fake) Create(ctx context.Context, spec CreateSpec) (string, error) {
	if err := f.fail("Create"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.info.Name == spec.Name {
			return "", fmt.Errorf("fake create %s: %w", spec.Name, ErrConflict)
		}
	}
	if !f.images[spec.Image] {
		return "", fmt.Errorf("fake create: image %s: %w", spec.Image, ErrNotFound)
	}
	f.nextID++
	id := fmt.Sprintf("c%011d", f.nextID)
	var keys []string
	for _, kv := range spec.Env {
		k, _, _ := bytes.Cut([]byte(kv), []byte("="))
		keys = append(keys, string(k))
	}
	var mounts []Mount
	for _, v := range spec.Volumes {
		mounts = append(mounts, Mount{Type: "volume", Source: v.Name, Destination: v.Target})
	}
	for _, t := range spec.Tmpfs {
		mounts = append(mounts, Mount{Type: "tmpfs", Destination: t.Target})
	}
	stop := spec.StopTimeout
	ip := ""
	if spec.NetworkMode != "host" {
		ip = fmt.Sprintf("172.20.0.%d", f.nextID+1)
	}
	f.containers[id] = &fakeContainer{
		info: ContainerInfo{
			ID: id, Name: spec.Name, Image: spec.Image, ImageID: "sha256:" + spec.Image, Created: f.now(), State: "created",
			Labels: spec.Labels, NetworkMode: spec.NetworkMode, IP: ip, NanoCPUs: spec.NanoCPUs, MemoryBytes: spec.MemoryBytes,
			PidsLimit: spec.PidsLimit, EnvKeys: keys, Mounts: mounts, Restart: spec.Restart, StopTimeout: &stop,
		},
		status: "Created",
	}
	f.created = append(f.created, spec)
	return id, nil
}

// change applies fn to a container that exists and sends the event of the action.
func (f *Fake) change(id, method, action string, fn func(c *fakeContainer) error) error {
	if err := f.fail(method); err != nil {
		return err
	}
	f.mu.Lock()
	c, ok := f.containers[id]
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("fake %s %s: %w", method, id, ErrNotFound)
	}
	err := fn(c)
	f.mu.Unlock()
	if err == nil && action != "" {
		select {
		case f.events <- Event{ContainerID: id, Action: action, Time: f.now()}:
		default:
		}
	}
	return err
}

func (f *Fake) Start(ctx context.Context, id string) error {
	return f.change(id, "Start", "start", func(c *fakeContainer) error {
		c.info.State, c.status, c.info.StartedAt, c.info.ExitCode = "running", "Up Less than a second", f.now(), 0
		return nil
	})
}

func (f *Fake) Stop(ctx context.Context, id string, timeout time.Duration) error {
	return f.change(id, "Stop", "die", func(c *fakeContainer) error {
		c.info.State, c.status, c.info.FinishedAt, c.info.ExitCode = "exited", "Exited (0) Less than a second ago", f.now(), 0
		return nil
	})
}

func (f *Fake) Kill(ctx context.Context, id string, signal string) error {
	action := "kill"
	return f.change(id, "Kill", action, func(c *fakeContainer) error {
		if c.info.State != "running" {
			return fmt.Errorf("fake kill %s: container is not running: %w", id, ErrConflict)
		}
		if signal == "SIGKILL" || signal == "" {
			c.info.State, c.status, c.info.FinishedAt, c.info.ExitCode = "exited", "Exited (137) Less than a second ago", f.now(), 137
		}
		return nil
	})
}

func (f *Fake) Restart(ctx context.Context, id string, timeout time.Duration) error {
	return f.change(id, "Restart", "restart", func(c *fakeContainer) error {
		c.info.State, c.status, c.info.StartedAt, c.info.ExitCode = "running", "Up Less than a second", f.now(), 0
		return nil
	})
}

func (f *Fake) Remove(ctx context.Context, id string) error {
	if err := f.fail("Remove"); err != nil {
		return err
	}
	f.mu.Lock()
	c, ok := f.containers[id]
	switch {
	case !ok:
		f.mu.Unlock()
		return fmt.Errorf("fake remove %s: %w", id, ErrNotFound)
	case c.info.State == "running" || c.info.State == "restarting":
		f.mu.Unlock()
		return fmt.Errorf("fake remove %s: container is running: %w", id, ErrConflict)
	}
	delete(f.containers, id)
	f.mu.Unlock()
	select {
	case f.events <- Event{ContainerID: id, Action: "destroy", Time: f.now()}:
	default:
	}
	return nil
}

func (f *Fake) Stats(ctx context.Context, id string) (<-chan Sample, error) {
	if err := f.fail("Stats"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	c, ok := f.containers[id]
	if !ok {
		f.mu.Unlock()
		return nil, fmt.Errorf("fake stats %s: %w", id, ErrNotFound)
	}
	in := make(chan Sample)
	c.stats = in
	f.mu.Unlock()

	out := make(chan Sample)
	go func() {
		defer close(out)
		for {
			select {
			case s := <-in:
				select {
				case out <- s:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (f *Fake) Events(ctx context.Context) (<-chan Event, <-chan error) {
	out := make(chan Event)
	errs := make(chan error, 1)
	if err := f.fail("Events"); err != nil {
		errs <- err
		close(out)
		return out, errs
	}
	go func() {
		defer close(out)
		for {
			select {
			case ev := <-f.events:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, errs
}

func (f *Fake) Logs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error) {
	if err := f.fail("Logs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return nil, fmt.Errorf("fake logs %s: %w", id, ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(c.logs)), nil
}

func (f *Fake) ImageExists(ctx context.Context, ref string) (bool, error) {
	if err := f.fail("ImageExists"); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[ref], nil
}

func (f *Fake) ImagePull(ctx context.Context, ref string) error {
	if err := f.fail("ImagePull"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	f.images[ref] = true
	return nil
}

func (f *Fake) ImageList(ctx context.Context, reference string) ([]Image, error) {
	if err := f.fail("ImageList"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Image
	for ref := range f.images {
		out = append(out, Image{ID: "sha256:" + ref, Tags: []string{ref}, Created: time.Unix(0, 0).UTC(), SizeBytes: 1 << 20})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *Fake) NetworkExists(ctx context.Context, name string) (bool, error) {
	if err := f.fail("NetworkExists"); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.networks[name]
	return ok, nil
}

func (f *Fake) NetworkCreate(ctx context.Context, name string, labels map[string]string) error {
	if err := f.fail("NetworkCreate"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[name] = labels
	return nil
}

func (f *Fake) VolumeCreate(ctx context.Context, name string, labels map[string]string) error {
	if err := f.fail("VolumeCreate"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.volumes[name]; !ok {
		f.volumes[name] = labels
	}
	return nil
}

func (f *Fake) VolumeLabels(ctx context.Context, name string) (map[string]string, error) {
	if err := f.fail("VolumeLabels"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	labels, ok := f.volumes[name]
	if !ok {
		return nil, fmt.Errorf("fake volume %s: %w", name, ErrNotFound)
	}
	return labels, nil
}

func (f *Fake) VolumeRemove(ctx context.Context, name string) error {
	if err := f.fail("VolumeRemove"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.volumes[name]; !ok {
		return fmt.Errorf("fake remove volume %s: %w", name, ErrNotFound)
	}
	for _, c := range f.containers {
		for _, m := range c.info.Mounts {
			if m.Source == name {
				return fmt.Errorf("fake remove volume %s: in use: %w", name, ErrConflict)
			}
		}
	}
	delete(f.volumes, name)
	return nil
}

func (f *Fake) VolumeUsage(ctx context.Context) ([]VolumeUsage, error) {
	if err := f.fail("VolumeUsage"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []VolumeUsage
	for name, labels := range f.volumes {
		size, ok := f.volumeSize[name]
		if !ok {
			size = -1
		}
		out = append(out, VolumeUsage{Name: name, Labels: labels, Size: size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

var _ Engine = (*Fake)(nil)
var _ Engine = (*Moby)(nil)
