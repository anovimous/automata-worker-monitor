// Package docker is the worker machine controller's view of the Docker Engine. It is the only package that imports
// the Docker SDK: the others use the Engine interface and the plain types below, so that their tests need no daemon
// (Fake is an Engine in memory).
package docker

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned for a container, image, volume or network that does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when Docker refuses an action in the current state (e.g. removing a running container).
var ErrConflict = errors.New("conflict")

// Engine is what the worker machine controller does with Docker. The containers it lists and watches are the workers
// created by the monitor (label automata.worker.managed-by), no others.
type Engine interface {
	// Ping checks that the daemon answers and returns the highest API version it supports.
	Ping(ctx context.Context) (apiVersion string, err error)
	Info(ctx context.Context) (Info, error)

	ListWorkerContainers(ctx context.Context) ([]ContainerSummary, error)
	Inspect(ctx context.Context, id string) (ContainerInfo, error)
	Create(ctx context.Context, spec CreateSpec) (id string, err error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Kill(ctx context.Context, id string, signal string) error
	Restart(ctx context.Context, id string, timeout time.Duration) error
	Remove(ctx context.Context, id string) error

	// Stats streams the samples of a running container until ctx is done or the container stops; the channel is
	// closed then.
	Stats(ctx context.Context, id string) (<-chan Sample, error)
	// Events streams the events of the worker containers until ctx is done; the error channel gets a value when the
	// stream breaks.
	Events(ctx context.Context) (<-chan Event, <-chan error)
	// Logs returns the log stream of a container, multiplexed as Docker writes it (see Demux).
	Logs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error)

	ImageExists(ctx context.Context, ref string) (bool, error)
	ImagePull(ctx context.Context, ref string) error
	ImageList(ctx context.Context, reference string) ([]Image, error)

	NetworkExists(ctx context.Context, name string) (bool, error)
	NetworkCreate(ctx context.Context, name string, labels map[string]string) error

	VolumeCreate(ctx context.Context, name string, labels map[string]string) error
	// VolumeLabels returns the labels of a volume, ErrNotFound when it does not exist.
	VolumeLabels(ctx context.Context, name string) (map[string]string, error)
	VolumeRemove(ctx context.Context, name string) error
	// VolumeUsage returns the volumes with their size (Docker walks them, so it is slow).
	VolumeUsage(ctx context.Context) ([]VolumeUsage, error)
}

// Info is what the controller reports of the daemon and the host it runs on.
type Info struct {
	ServerVersion string
	APIVersion    string
	NCPU          int
	MemTotal      int64
	KernelVersion string
}

// ContainerSummary is a container of a list.
type ContainerSummary struct {
	ID     string
	Name   string
	Labels map[string]string
	State  string
	Status string // Docker's text, e.g. "Up 44 minutes (healthy)"
}

// ContainerInfo is what the controller keeps of docker inspect.
type ContainerInfo struct {
	ID           string
	Name         string
	Image        string // as given at creation, e.g. automata-worker:0.10.0
	ImageID      string
	Created      time.Time
	State        string // created, running, paused, restarting, removing, exited, dead
	Health       string // healthy, unhealthy, starting, "" without a healthcheck
	ExitCode     int
	StartedAt    time.Time // zero when it never started
	FinishedAt   time.Time // zero while it never stopped
	RestartCount int
	OOMKilled    bool
	Labels       map[string]string
	NetworkMode  string
	IP           string // on the first network; "" in host mode
	NanoCPUs     int64  // 0: no limit
	MemoryBytes  int64  // 0: no limit
	PidsLimit    int64  // 0: no limit
	EnvKeys      []string
	Mounts       []Mount
	Restart      RestartPolicy
	StopTimeout  *int
}

// Running tells whether the container's processes run (a paused container counts as running for Docker stats).
func (c ContainerInfo) Running() bool {
	return c.State == "running"
}

type Mount struct {
	Type        string // volume, tmpfs
	Source      string // the volume name
	Destination string
}

type RestartPolicy struct {
	Name       string
	MaxRetries int
}

// CreateSpec is a container to create. Only named volumes and tmpfs can be mounted: the controller never bind-mounts
// a host path into a worker.
type CreateSpec struct {
	Name        string
	Image       string
	Env         []string
	Labels      map[string]string
	NetworkMode string
	ExtraHosts  []string
	NanoCPUs    int64
	MemoryBytes int64
	PidsLimit   int64
	Restart     RestartPolicy
	StopTimeout int
	Privileged  bool
	Volumes     []VolumeMount
	Tmpfs       []TmpfsMount
	LogMaxSize  string
	LogMaxFile  string
}

type VolumeMount struct {
	Name   string
	Target string
}

type TmpfsMount struct {
	Target    string
	SizeBytes int64
	Mode      uint32
}

// Sample is one Docker stats sample, raw counters only.
type Sample struct {
	Read            time.Time
	CPUTotalNs      uint64
	CPUThrottledNs  uint64
	MemUsage        uint64
	MemInactiveFile uint64
	MemLimit        uint64
	Pids            uint64
	PidsLimit       uint64
	BlockRead       uint64
	BlockWrite      uint64
}

// Event is a Docker event of a worker container.
type Event struct {
	ContainerID string
	Action      string // create, start, die, oom, kill, stop, destroy, health_status: healthy, restart, ...
	Time        time.Time
}

type LogOptions struct {
	Tail       string // a number or "all"
	Since      string // RFC 3339 or a Unix time
	Follow     bool
	Timestamps bool
}

type Image struct {
	ID        string
	Tags      []string
	Created   time.Time
	SizeBytes int64
}

type VolumeUsage struct {
	Name   string
	Labels map[string]string
	Size   int64 // -1 when Docker does not know
}
