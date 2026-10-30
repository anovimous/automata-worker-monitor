// Package workerapi holds the Go types of the telemetry contract between a worker and the monitor
// (plan/05-telemetry-contract.md): the status document served on GET /status and the container labels.
// The samples in contracts/ are the reference; the tests parse them.
package workerapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SchemaVersion is the version of the status document these types describe.
const SchemaVersion = 1

// ErrUnsupportedSchema is returned by Parse for a document of another schema version; its text is the reason code
// shown by the monitor.
var ErrUnsupportedSchema = errors.New("UNSUPPORTED_SCHEMA")

type Phase string

const (
	PhaseStarting     Phase = "STARTING"
	PhaseIdle         Phase = "IDLE"
	PhasePreparing    Phase = "PREPARING"
	PhaseStartingJob  Phase = "STARTING_JOB"
	PhaseRunningJob   Phase = "RUNNING_JOB"
	PhaseFinishing    Phase = "FINISHING"
	PhaseDrained      Phase = "DRAINED"
	PhaseShuttingDown Phase = "SHUTTING_DOWN"
)

// Status is the status document, schema version 1. Optional objects are pointers so that they are written as null,
// never left out.
type Status struct {
	SchemaVersion      int            `json:"schemaVersion"`
	WorkerID           string         `json:"workerId"`
	Version            string         `json:"version"`
	Commit             string         `json:"commit"`
	GoVersion          string         `json:"goVersion"`
	PID                int            `json:"pid"`
	StartedAt          time.Time      `json:"startedAt"`
	Now                time.Time      `json:"now"`
	Phase              Phase          `json:"phase"`
	PhaseDetail        string         `json:"phaseDetail"`
	PhaseSince         time.Time      `json:"phaseSince"`
	PhaseBudgetSeconds int            `json:"phaseBudgetSeconds"`
	LastProgressAt     time.Time      `json:"lastProgressAt"`
	Draining           bool           `json:"draining"`
	ShuttingDown       bool           `json:"shuttingDown"`
	Job                *Job           `json:"job"`
	Dependencies       Dependencies   `json:"dependencies"`
	WordlistCache      *WordlistCache `json:"wordlistCache"`
	Counters           Counters       `json:"counters"`
}

// Job is the job the worker is busy with, as the worker knows it.
type Job struct {
	JobID      int64         `json:"jobId"`
	RoutineKey string        `json:"routineKey"`
	Command    string        `json:"command"`
	State      string        `json:"state"` // "" before RUNNING was reported, then RUNNING or PAUSED
	TakenAt    time.Time     `json:"takenAt"`
	StartedAt  *time.Time    `json:"startedAt"`
	Connector  *Connector    `json:"connector"`
	Resources  *JobResources `json:"resources"`
}

type Connector struct {
	Connected                bool       `json:"connected"`
	LastMessageAt            *time.Time `json:"lastMessageAt"`
	LastHeartbeatAt          *time.Time `json:"lastHeartbeatAt"`
	HeartbeatIntervalSeconds int        `json:"heartbeatIntervalSeconds"`
	HeartbeatTimeoutSeconds  int        `json:"heartbeatTimeoutSeconds"`
}

// JobResources is a sample of the job's cgroups; Job or Tool is null when its cgroup can not be read.
type JobResources struct {
	SampledAt time.Time    `json:"sampledAt"`
	Job       *CgroupUsage `json:"job"`
	Tool      *CgroupUsage `json:"tool"`
}

// CgroupUsage is what a cgroup used; a value whose cgroup controller is not enabled is null, not 0.
type CgroupUsage struct {
	CPUUsageSeconds float64 `json:"cpuUsageSeconds"`
	MemoryBytes     *int64  `json:"memoryBytes"`
	MemoryPeakBytes *int64  `json:"memoryPeakBytes"`
	Pids            *int64  `json:"pids"`
}

type Dependencies struct {
	RabbitMQ      Dependency `json:"rabbitmq"`
	Core          Dependency `json:"core"`
	ObjectStorage Dependency `json:"objectStorage"`
}

// Dependency is a service as the worker sees it. Up is null for a service that is not a connection.
type Dependency struct {
	Up            *bool      `json:"up"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
	LastErrorAt   *time.Time `json:"lastErrorAt"`
	LastError     string     `json:"lastError"`
	ErrorsTotal   int64      `json:"errorsTotal"`
}

type WordlistCache struct {
	Dir         string `json:"dir"`
	Bytes       int64  `json:"bytes"`
	Files       int    `json:"files"`
	MaxBytes    int64  `json:"maxBytes"`
	PinnedFiles int    `json:"pinnedFiles"`
}

type Counters struct {
	MessagesTaken   int64            `json:"messagesTaken"`
	MessagesSkipped int64            `json:"messagesSkipped"`
	JobsStarted     int64            `json:"jobsStarted"`
	JobsByOutcome   map[string]int64 `json:"jobsByOutcome"`
}

// Parse reads a status document. Unknown fields are ignored (a newer worker may add some); a schema version other
// than SchemaVersion is refused with ErrUnsupportedSchema.
func Parse(b []byte) (Status, error) {
	var head struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return Status{}, fmt.Errorf("failed to read the status document: %w", err)
	}
	if head.SchemaVersion != SchemaVersion {
		return Status{}, fmt.Errorf("%w: schema version %d, want %d", ErrUnsupportedSchema, head.SchemaVersion, SchemaVersion)
	}

	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return Status{}, fmt.Errorf("failed to read the status document: %w", err)
	}
	return s, nil
}

// ProgressAge is how long the worker's loop has gone without progress. It uses the document's own now, so the
// clocks of the worker and of the monitor need not agree.
func (s Status) ProgressAge() time.Duration {
	return s.Now.Sub(s.LastProgressAt)
}
