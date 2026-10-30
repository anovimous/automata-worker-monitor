// Package controllerapi holds the request and response types of the worker machine controller's API
// (plan/06-worker-machine-controller.md §3), shared by the worker machine controller and the workers control plane.
// The samples contracts/worker-machine-controller-*.sample.json are the reference; the tests round-trip them.
package controllerapi

import (
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// Error codes of the API, with their HTTP status.
const (
	CodeUnauthorized    = "UNAUTHORIZED"      // 401
	CodeNotFound        = "NOT_FOUND"         // 404
	CodeConflict        = "CONFLICT"          // 409, e.g. remove while running
	CodeInvalid         = "INVALID"           // 400
	CodeImageNotAllowed = "IMAGE_NOT_ALLOWED" // 400
	CodeDocker          = "DOCKER"            // 502
)

// ErrorResponse is the body of every error: {"error": {"code": "NOT_FOUND", "message": "..."}}.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Flags of a worker observation.
const (
	FlagDuplicateID = "DUPLICATE_ID" // another container carries the same worker id
	FlagNoTelemetry = "NO_TELEMETRY" // the container has no telemetry port label
)

// HealthResponse is the body of GET /v1/health.
type HealthResponse struct {
	HostName                       string       `json:"hostName"`
	WorkerMachineControllerVersion string       `json:"workerMachineControllerVersion"`
	Docker                         DockerEngine `json:"docker"`
	Now                            time.Time    `json:"now"`
}

type DockerEngine struct {
	Version    string `json:"version"`
	APIVersion string `json:"apiVersion"`
}

// Snapshot is the body of GET /v1/snapshot: everything the workers control plane polls from one host.
type Snapshot struct {
	HostName                       string              `json:"hostName"`
	WorkerMachineControllerVersion string              `json:"workerMachineControllerVersion"`
	Now                            time.Time           `json:"now"`
	Host                           HostMetrics         `json:"host"`
	Workers                        []WorkerObservation `json:"workers"`
}

type HostMetrics struct {
	SampledAt            time.Time `json:"sampledAt"`
	CPUs                 int       `json:"cpus"`
	CPUUsageRatio        float64   `json:"cpuUsageRatio"`
	Load1                float64   `json:"load1"`
	Load5                float64   `json:"load5"`
	Load15               float64   `json:"load15"`
	MemoryTotalBytes     int64     `json:"memoryTotalBytes"`
	MemoryAvailableBytes int64     `json:"memoryAvailableBytes"`
	SwapUsedBytes        int64     `json:"swapUsedBytes"`
	Disks                []Disk    `json:"disks"`
	// VolumesBytes is the total size of the worker volumes on the host; null until the first volume sample.
	VolumesBytes  *int64             `json:"volumesBytes"`
	Networks      []NetworkInterface `json:"networks"`
	UptimeSeconds int64              `json:"uptimeSeconds"`
	Kernel        string             `json:"kernel"`
	DockerVersion string             `json:"dockerVersion"`
}

type Disk struct {
	Path       string `json:"path"`
	TotalBytes int64  `json:"totalBytes"`
	FreeBytes  int64  `json:"freeBytes"`
}

type NetworkInterface struct {
	Interface     string  `json:"interface"`
	RxBytesTotal  int64   `json:"rxBytesTotal"`
	TxBytesTotal  int64   `json:"txBytesTotal"`
	RxBytesPerSec float64 `json:"rxBytesPerSec"`
	TxBytesPerSec float64 `json:"txBytesPerSec"`
}

// WorkerObservation is what the worker machine controller knows of one worker container.
type WorkerObservation struct {
	WorkerID  string     `json:"workerId"`
	Managed   bool       `json:"managed"`
	Template  string     `json:"template"`
	Container Container  `json:"container"`
	Resources *Resources `json:"resources"` // null when the container is not running
	Volumes   *Volumes   `json:"volumes"`   // null until the first volume sample
	Telemetry Telemetry  `json:"telemetry"`
	Flags     []string   `json:"flags"`
}

type Container struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	State        string            `json:"state"`  // Docker's state: created, running, restarting, exited, paused, dead
	Status       string            `json:"status"` // Docker's text, e.g. "Up 44 minutes (healthy)"
	Health       string            `json:"health"` // Docker health: healthy, unhealthy, starting, or "" without a healthcheck
	ExitCode     int               `json:"exitCode"`
	StartedAt    *time.Time        `json:"startedAt"`
	FinishedAt   *time.Time        `json:"finishedAt"`
	RestartCount int               `json:"restartCount"`
	OOMKilled    bool              `json:"oomKilled"`
	OOMEvents    int               `json:"oomEvents"` // since the worker machine controller started
	NetworkMode  string            `json:"networkMode"`
	IP           string            `json:"ip"`
	Labels       map[string]string `json:"labels"`
	Limits       Limits            `json:"limits"`
}

// Limits of a container; null when there is none.
type Limits struct {
	CPUs        *float64 `json:"cpus"`
	MemoryBytes *int64   `json:"memoryBytes"`
	Pids        *int64   `json:"pids"`
}

// Resources is the latest Docker stats sample of a running container (plan/06 §4). The network fields are null in
// host network mode.
type Resources struct {
	SampledAt                time.Time `json:"sampledAt"`
	CPUCores                 float64   `json:"cpuCores"`
	CPUUsageSecondsTotal     float64   `json:"cpuUsageSecondsTotal"`
	CPUThrottledSecondsTotal float64   `json:"cpuThrottledSecondsTotal"`
	MemoryWorkingSetBytes    int64     `json:"memoryWorkingSetBytes"`
	MemoryUsageBytes         int64     `json:"memoryUsageBytes"`
	MemoryLimitBytes         int64     `json:"memoryLimitBytes"`
	Pids                     int64     `json:"pids"`
	PidsLimit                *int64    `json:"pidsLimit"`
	BlockReadBytesTotal      int64     `json:"blockReadBytesTotal"`
	BlockWriteBytesTotal     int64     `json:"blockWriteBytesTotal"`
	NetRxBytesTotal          *int64    `json:"netRxBytesTotal"`
	NetTxBytesTotal          *int64    `json:"netTxBytesTotal"`
	NetRxBytesPerSec         *float64  `json:"netRxBytesPerSec"`
	NetTxBytesPerSec         *float64  `json:"netTxBytesPerSec"`
}

// Volumes is the latest size sample of a worker's volumes (every 60 s).
type Volumes struct {
	SampledAt time.Time `json:"sampledAt"`
	Items     []Volume  `json:"items"`
}

type Volume struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"` // /var/lib/automata/tmp is the worker's work dir
	Bytes     *int64 `json:"bytes"`     // null when Docker gives no size
}

// Telemetry is the latest scrape of the worker's GET /status.
type Telemetry struct {
	Configured          bool              `json:"configured"`
	URL                 string            `json:"url"`
	OK                  bool              `json:"ok"`
	FetchedAt           *time.Time        `json:"fetchedAt"`
	LatencyMs           *int64            `json:"latencyMs"`
	ConsecutiveFailures int               `json:"consecutiveFailures"`
	LastError           string            `json:"lastError"`
	Status              *workerapi.Status `json:"status"`
}

// WorkerDetail is the body of GET /v1/workers/{id}: the observation and a subset of docker inspect.
type WorkerDetail struct {
	WorkerObservation
	Details ContainerDetails `json:"details"`
}

type ContainerDetails struct {
	ImageID       string        `json:"imageId"`
	Created       time.Time     `json:"created"`
	EnvKeys       []string      `json:"envKeys"` // keys only: the values hold the worker's credentials
	Mounts        []Mount       `json:"mounts"`
	RestartPolicy RestartPolicy `json:"restartPolicy"`
	StopTimeout   *int          `json:"stopTimeoutSeconds"`
}

type Mount struct {
	Type        string `json:"type"` // volume or tmpfs
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// CreateWorkerRequest is the body of POST /v1/workers.
type CreateWorkerRequest struct {
	WorkerID string     `json:"workerId"`
	Template WorkerSpec `json:"template"`
}

// WorkerSpec is the template a worker is created from, as of the create request.
type WorkerSpec struct {
	Name                  string            `json:"name"`
	Image                 string            `json:"image"`
	PullPolicy            string            `json:"pullPolicy"` // always, if-missing, never
	EnvFile               string            `json:"envFile"`    // name of an env file on the host, never its content
	Env                   map[string]string `json:"env"`        // non-secret overrides
	Network               string            `json:"network"`    // host or a network name
	ExtraHosts            []string          `json:"extraHosts"`
	CPUs                  float64           `json:"cpus"`
	MemoryBytes           int64             `json:"memoryBytes"`
	PidsLimit             int64             `json:"pidsLimit"`
	WordlistCacheMaxBytes int64             `json:"wordlistCacheMaxBytes"`
	TmpfsSocketsBytes     int64             `json:"tmpfsSocketsBytes"`
	RestartPolicy         RestartPolicy     `json:"restartPolicy"`
	StopTimeoutSeconds    int               `json:"stopTimeoutSeconds"`
	ExtraLabels           map[string]string `json:"extraLabels"`
}

// Pull policies of a WorkerSpec.
const (
	PullAlways    = "always"
	PullIfMissing = "if-missing"
	PullNever     = "never"
)

type RestartPolicy struct {
	Name       string `json:"name"` // no, on-failure, unless-stopped, always
	MaxRetries int    `json:"maxRetries"`
}

// CreateWorkerResponse is the body of a 201 from POST /v1/workers.
type CreateWorkerResponse struct {
	WorkerID     string `json:"workerId"`
	ContainerID  string `json:"containerId"`
	TelemetryURL string `json:"telemetryUrl"`
}

// SignalRequest is the body of POST /v1/workers/{id}/signal; only SignalDrain and SignalUndrain are accepted.
type SignalRequest struct {
	Signal string `json:"signal"`
}

const (
	SignalDrain   = "SIGUSR1"
	SignalUndrain = "SIGUSR2"
)

// StopRequest is the body of POST /v1/workers/{id}/stop and /restart.
type StopRequest struct {
	TimeoutSeconds int `json:"timeoutSeconds"` // 1–600
}

// RemoveResponse is the body of DELETE /v1/workers/{id}: a volume that could not be removed does not fail the call,
// since the container is already gone.
type RemoveResponse struct {
	VolumesNotRemoved []VolumeError `json:"volumesNotRemoved"`
}

type VolumeError struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// LogLine is one line of GET /v1/workers/{id}/logs, already redacted.
type LogLine struct {
	TS     time.Time `json:"ts"`
	Stream string    `json:"stream"` // stdout or stderr
	Line   string    `json:"line"`
}

// Image is one entry of GET /v1/images.
type Image struct {
	ID        string    `json:"id"`
	Tags      []string  `json:"tags"`
	Created   time.Time `json:"created"`
	SizeBytes int64     `json:"sizeBytes"`
}
