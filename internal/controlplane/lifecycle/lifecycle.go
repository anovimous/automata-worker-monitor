// Package lifecycle is the state machine of what the operator asked for and how far it got
// (plan/07-workers-control-plane.md §5). It is kept apart from the health on purpose: a worker can be ACTIVE (wanted
// running) and DOWN (crashed).
package lifecycle

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	Provisioning    = "PROVISIONING"
	ProvisionFailed = "PROVISION_FAILED"
	Active          = "ACTIVE"
	Draining        = "DRAINING"
	Stopping        = "STOPPING"
	Stopped         = "STOPPED"
	Removing        = "REMOVING"
	Removed         = "REMOVED"
)

// Actions an operator can run.
const (
	ActionDrain   = "drain"
	ActionUndrain = "undrain"
	ActionStop    = "stop"
	ActionKill    = "kill"
	ActionRestart = "restart"
	ActionStart   = "start"
	ActionRemove  = "remove"
)

// Reasons of a lifecycle.
const (
	ReasonDrained          = "DRAINED"
	ReasonDrainTimeout     = "DRAIN_TIMEOUT"
	ReasonExitedCleanly    = "EXITED_CLEANLY"
	ReasonDisappeared      = "DISAPPEARED"
	ReasonProvisionTimeout = "PROVISION_TIMEOUT"
)

// a running worker without telemetry counts as provisioned after this long
const NoTelemetryActiveAfter = 30 * time.Second

// ErrHostCordoned refuses a start on a cordoned host.
var ErrHostCordoned = errors.New("HOST_CORDONED")

// TransitionError refuses an action that the lifecycle does not allow; it names the allowed ones.
type TransitionError struct {
	Lifecycle string
	Action    string
	Allowed   []string
}

func (e *TransitionError) Error() string {
	allowed := "none"
	if len(e.Allowed) > 0 {
		allowed = strings.Join(e.Allowed, ", ")
	}
	return fmt.Sprintf("INVALID_TRANSITION: %s is not allowed in %s (allowed: %s)", e.Action, e.Lifecycle, allowed)
}

var allowed = map[string][]string{
	ProvisionFailed: {ActionRemove},
	Active:          {ActionDrain, ActionStop, ActionKill, ActionRestart},
	Draining:        {ActionUndrain, ActionStop, ActionKill},
	Stopping:        {ActionKill},
	Stopped:         {ActionStart, ActionRemove},
}

// the actions that need a running container: Docker refuses to signal or kill one that is not (e.g. a worker that
// crashed and stays exited is still ACTIVE); stop and restart work on an exited container too
var needRunning = map[string]bool{ActionDrain: true, ActionUndrain: true, ActionKill: true}

// Allowed lists the actions of a lifecycle; start is left out on a cordoned host, and the actions that need a running
// container when it does not run.
func Allowed(lc string, cordoned, running bool) []string {
	out := []string{}
	for _, a := range allowed[lc] {
		if a == ActionStart && cordoned || needRunning[a] && !running {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Check tells whether an action may run.
func Check(lc, action string, cordoned, running bool) error {
	for _, a := range allowed[lc] {
		if a == action {
			if a == ActionStart && cordoned {
				return fmt.Errorf("%w: uncordon the host to start a worker on it", ErrHostCordoned)
			}
			if needRunning[a] && !running {
				break
			}
			return nil
		}
	}
	return &TransitionError{Lifecycle: lc, Action: action, Allowed: Allowed(lc, cordoned, running)}
}

// AfterAction is the lifecycle once an action was sent to the worker machine controller.
func AfterAction(action string) string {
	switch action {
	case ActionDrain:
		return Draining
	case ActionUndrain, ActionRestart, ActionStart:
		return Active
	case ActionStop, ActionKill:
		return Stopping
	case ActionRemove:
		return Removing
	}
	return ""
}

// Observation is what a poll saw of a worker, for the transitions that follow the observations.
type Observation struct {
	Now            time.Time
	LifecycleSince time.Time
	// Missing tells that the container is not in the snapshot
	Missing bool
	Running bool
	// Exited tells that the container stopped and Docker does not restart it
	Exited   bool
	ExitCode int
	// StatusOK tells that the status of the worker was read
	StatusOK bool
	// TelemetryConfigured is false for a container without a telemetry port
	TelemetryConfigured bool
	// RunningSince is when the container started (zero when unknown)
	RunningSince     time.Time
	ProvisionTimeout time.Duration
}

// Observe returns the lifecycle after an observation, with its reason, and whether it changed.
func Observe(lc string, o Observation) (next, reason string, changed bool) {
	switch lc {
	case Provisioning:
		switch {
		case o.Running && o.StatusOK:
			return Active, "", true
		case o.Running && !o.TelemetryConfigured && !o.RunningSince.IsZero() && o.Now.Sub(o.RunningSince) >= NoTelemetryActiveAfter:
			return Active, "", true
		case o.Now.Sub(o.LifecycleSince) > o.ProvisionTimeout:
			return ProvisionFailed, ReasonProvisionTimeout, true
		}
	case Removing:
		if o.Missing {
			return Removed, "", true
		}
	case Removed, ProvisionFailed:
	default:
		if o.Missing {
			// removed outside the monitor
			return Removed, ReasonDisappeared, true
		}
		switch lc {
		case Draining:
			if o.Exited && o.ExitCode == 0 {
				return Stopped, ReasonDrained, true
			}
		case Stopping:
			if !o.Running {
				return Stopped, "", true
			}
		case Active:
			// e.g. a SIGUSR1 sent by hand: the worker drained and exited by itself
			if o.Exited && o.ExitCode == 0 {
				return Stopped, ReasonExitedCleanly, true
			}
		}
	}
	return lc, "", false
}
