// Package health decides the health of a worker from what was observed. It is
// one pure function: the first rule that matches gives the health, and the degradations are collected as reasons.
package health

import (
	"fmt"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
)

type Health string

const (
	Healthy      Health = "HEALTHY"
	Degraded     Health = "DEGRADED"
	Stuck        Health = "STUCK"
	Unresponsive Health = "UNRESPONSIVE"
	Down         Health = "DOWN"
	Starting     Health = "STARTING"
	Unknown      Health = "UNKNOWN"
	None         Health = "NONE" // not evaluated: the lifecycle says it all
)

// Reason codes.
const (
	ReasonControllerUnreachable = "WORKER_MACHINE_CONTROLLER_UNREACHABLE"
	ReasonContainerMissing      = "CONTAINER_MISSING"
	ReasonContainerRestarting   = "CONTAINER_RESTARTING"
	ReasonContainerExited       = "CONTAINER_EXITED"
	ReasonOOMKilled             = "OOM_KILLED"
	ReasonNoTelemetry           = "NO_TELEMETRY"
	ReasonNoProgress            = "NO_PROGRESS"
	ReasonRabbitMQDown          = "RABBITMQ_DOWN"
	ReasonCoreErrors            = "CORE_ERRORS"
	ReasonObjectStorageErrors   = "OBJECT_STORAGE_ERRORS"
	ReasonConnectorSilent       = "CONNECTOR_SILENT"
	ReasonMemoryNearLimit       = "MEMORY_NEAR_LIMIT"
	ReasonRestartedRecently     = "RESTARTED_RECENTLY"
	ReasonSlowStatus            = "SLOW_STATUS"
	ReasonDuplicateID           = "DUPLICATE_ID"
	ReasonDockerUnhealthy       = "DOCKER_UNHEALTHY"
)

// Input is what is known of a worker at one poll.
type Input struct {
	Now       time.Time // the control plane's clock
	Lifecycle string
	// ControllerDownSince is when the worker machine controller of the host stopped answering; zero while it answers
	ControllerDownSince time.Time
	// Observation is the worker in the last snapshot; nil when its container is missing
	Observation *controllerapi.WorkerObservation
	// RestartedRecently tells whether the container restarted within the restart window
	RestartedRecently bool
	// Previous is the result of the previous poll (empty for the first one)
	Previous Result
}

// Result is the health of a worker.
type Result struct {
	Health      Health
	Reasons     []string
	Message     string
	Responsive  bool
	ProgressAge *time.Duration
	// Candidate is what the rules gave, before the hysteresis: a change to DEGRADED needs the same candidate twice
	Candidate Health
}

// Evaluate decides the health of a worker.
func Evaluate(in Input, cfg config.Health) Result {
	r := evaluate(in, cfg)
	r.Candidate = r.Health
	// a degradation must hold for two polls before it shows, so that one slow scrape does not flap the badge
	if r.Health == Degraded && in.Previous.Health != Degraded && in.Previous.Candidate != Degraded && in.Previous.Health != "" {
		kept := in.Previous
		kept.Candidate = Degraded
		kept.ProgressAge = r.ProgressAge
		return kept
	}
	r.Responsive = r.Health == Healthy || r.Health == Degraded
	if r.Reasons == nil {
		r.Reasons = []string{}
	}
	return r
}

func evaluate(in Input, cfg config.Health) Result {
	switch in.Lifecycle {
	case lifecycle.Stopped, lifecycle.Removing, lifecycle.Removed, lifecycle.ProvisionFailed:
		return Result{Health: None}
	}

	if !in.ControllerDownSince.IsZero() {
		down := in.Now.Sub(in.ControllerDownSince)
		if down >= seconds(cfg.WorkerMachineControllerUnknownAfterSeconds) {
			return Result{Health: Unknown, Reasons: []string{ReasonControllerUnreachable},
				Message: fmt.Sprintf("The worker machine controller of the host has not answered for %d s.", int(down.Seconds()))}
		}
		// a short outage keeps what was known, so that a missed poll does not flap the health
		if in.Previous.Health != "" {
			return in.Previous
		}
		return Result{Health: Unknown}
	}

	o := in.Observation
	provisioning := in.Lifecycle == lifecycle.Provisioning
	if o == nil {
		if provisioning {
			return Result{Health: Starting, Message: "The container is being created."}
		}
		return Result{Health: Down, Reasons: []string{ReasonContainerMissing}, Message: "The container of the worker is missing."}
	}

	c := o.Container
	switch c.State {
	case "running":
	case "restarting":
		return Result{Health: Down, Reasons: []string{ReasonContainerRestarting}, Message: "Docker is restarting the container."}
	default:
		if provisioning && c.State == "created" {
			return Result{Health: Starting, Message: "The container is created and not started yet."}
		}
		r := Result{Health: Down, Reasons: []string{ReasonContainerExited},
			Message: fmt.Sprintf("The container is %s with exit code %d (EXIT_CODE_%d).", c.State, c.ExitCode, c.ExitCode)}
		if c.OOMKilled {
			r.Reasons = append(r.Reasons, ReasonOOMKilled)
			r.Message += " It was killed for using too much memory."
		}
		return r
	}

	t := o.Telemetry
	if !t.Configured {
		r := Result{Health: Healthy, Reasons: []string{ReasonNoTelemetry}, Message: "The worker has no telemetry; only Docker is watched."}
		if c.Health != "" && c.Health != "healthy" {
			r.Health = Unresponsive
			r.Message = fmt.Sprintf("The worker has no telemetry and Docker says it is %s.", c.Health)
		}
		return r
	}

	if t.FetchedAt == nil && (c.StartedAt == nil || in.Now.Sub(*c.StartedAt) < seconds(cfg.StartingGraceSeconds)) {
		return Result{Health: Starting, Message: "The worker is starting; its status has not been read yet."}
	}

	if t.ConsecutiveFailures >= cfg.UnresponsiveAfterFailures {
		code := t.LastErrorCode
		if code == "" {
			code = controllerapi.ScrapeUnreachable
		}
		return Result{Health: Unresponsive, Reasons: []string{code},
			Message: fmt.Sprintf("The status of the worker could not be read %d times in a row (%s).", t.ConsecutiveFailures, code)}
	}

	s := t.Status
	if s == nil {
		// some failures, but fewer than the threshold, and no status ever: still starting as far as is known
		return Result{Health: Starting, Message: "The status of the worker has not been read yet."}
	}
	age := s.ProgressAge()
	budget := seconds(s.PhaseBudgetSeconds)
	if age > budget {
		detail := ""
		if s.PhaseDetail != "" {
			detail = " (" + s.PhaseDetail + ")"
		}
		return Result{Health: Stuck, Reasons: []string{ReasonNoProgress}, ProgressAge: &age,
			Message: fmt.Sprintf("The main loop has not made progress for %d s in phase %s%s; its budget is %d s.",
				int(age.Seconds()), s.Phase, detail, s.PhaseBudgetSeconds)}
	}

	reasons := degradations(in, o, cfg)
	if len(reasons) == 0 {
		return Result{Health: Healthy, ProgressAge: &age}
	}
	return Result{Health: Degraded, Reasons: reasons, ProgressAge: &age, Message: Describe(reasons)}
}

// degradations are the reasons for DEGRADED of a worker whose loop makes progress.
func degradations(in Input, o *controllerapi.WorkerObservation, cfg config.Health) []string {
	var reasons []string
	s := o.Telemetry.Status
	d := s.Dependencies
	if d.RabbitMQ.Up != nil && !*d.RabbitMQ.Up {
		reasons = append(reasons, ReasonRabbitMQDown)
	}
	recent := seconds(cfg.CoreErrorRecentSeconds)
	failing := func(lastError, lastSuccess *time.Time) bool {
		return lastError != nil && s.Now.Sub(*lastError) <= recent && (lastSuccess == nil || lastSuccess.Before(*lastError))
	}
	if failing(d.Core.LastErrorAt, d.Core.LastSuccessAt) {
		reasons = append(reasons, ReasonCoreErrors)
	}
	if failing(d.ObjectStorage.LastErrorAt, d.ObjectStorage.LastSuccessAt) {
		reasons = append(reasons, ReasonObjectStorageErrors)
	}
	if s.Phase == "RUNNING_JOB" && s.Job != nil && s.Job.Connector != nil && s.Job.Connector.LastMessageAt != nil {
		c := s.Job.Connector
		if s.Now.Sub(*c.LastMessageAt) > time.Duration(cfg.DegradedHeartbeatFactor)*seconds(c.HeartbeatIntervalSeconds) {
			reasons = append(reasons, ReasonConnectorSilent)
		}
	}
	if r, limit := o.Resources, o.Container.Limits.MemoryBytes; r != nil && limit != nil && *limit > 0 &&
		float64(r.MemoryWorkingSetBytes)/float64(*limit) >= cfg.DegradedMemoryRatio {
		reasons = append(reasons, ReasonMemoryNearLimit)
	}
	if in.RestartedRecently {
		reasons = append(reasons, ReasonRestartedRecently)
	}
	if l := o.Telemetry.LatencyMs; l != nil && *l > int64(cfg.SlowStatusMs) {
		reasons = append(reasons, ReasonSlowStatus)
	}
	for _, f := range o.Flags {
		if f == controllerapi.FlagDuplicateID {
			reasons = append(reasons, ReasonDuplicateID)
		}
	}
	if o.Container.Health == "unhealthy" {
		reasons = append(reasons, ReasonDockerUnhealthy)
	}
	return reasons
}

var sentences = map[string]string{
	ReasonRabbitMQDown:        "The worker lost its connection to RabbitMQ.",
	ReasonCoreErrors:          "The worker's calls to the core are failing.",
	ReasonObjectStorageErrors: "The worker's calls to the object storage are failing.",
	ReasonConnectorSilent:     "The connector of the running job has gone silent.",
	ReasonMemoryNearLimit:     "The container is near its memory limit.",
	ReasonRestartedRecently:   "The container restarted recently.",
	ReasonSlowStatus:          "The worker answers its status slowly.",
	ReasonDuplicateID:         "Another container carries the same worker id.",
	ReasonDockerUnhealthy:     "Docker says the container is unhealthy although its status is fine.",
}

// Describe turns degradation reasons into sentences.
func Describe(reasons []string) string {
	var out []string
	for _, r := range reasons {
		if s, ok := sentences[r]; ok {
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}

// Same tells whether two results show the same thing (health and reasons), the condition of a HEALTH_CHANGED event.
func Same(a, b Result) bool {
	if a.Health != b.Health || len(a.Reasons) != len(b.Reasons) {
		return false
	}
	for i := range a.Reasons {
		if a.Reasons[i] != b.Reasons[i] {
			return false
		}
	}
	return true
}

func seconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
