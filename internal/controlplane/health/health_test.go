package health

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

var cfg = config.Defaults().Health

// running is the running worker of the snapshot sample, its status time moved to now.
func running(t *testing.T, now time.Time) *controllerapi.WorkerObservation {
	t.Helper()
	var snap controllerapi.Snapshot
	contracttest.DecodeStrict(t, contracttest.Read(t, "worker-machine-controller-snapshot.sample.json"), &snap)
	o := snap.Workers[0]
	s := o.Telemetry.Status
	shift := now.Sub(s.Now)
	s.Now = now
	s.LastProgressAt = s.LastProgressAt.Add(shift)
	s.Job.Connector.LastMessageAt = ptr(s.Job.Connector.LastMessageAt.Add(shift))
	// the sample's core error is 30 minutes old: not recent
	return &o
}

func ptr[T any](v T) *T { return &v }

var now = time.Date(2026, 10, 29, 10, 42, 14, 0, time.UTC)

func input(o *controllerapi.WorkerObservation) Input {
	return Input{Now: now, Lifecycle: lifecycle.Active, Observation: o}
}

func TestAHealthyWorker(t *testing.T) {
	r := Evaluate(input(running(t, now)), cfg)
	if r.Health != Healthy || len(r.Reasons) != 0 || !r.Responsive || r.ProgressAge == nil || *r.ProgressAge != 2*time.Second {
		t.Errorf("result = %+v", r)
	}
}

func TestRules(t *testing.T) {
	tests := []struct {
		name    string
		in      func() Input
		health  Health
		reasons []string
	}{
		{"stopped is not evaluated", func() Input {
			in := input(running(t, now))
			in.Lifecycle = lifecycle.Stopped
			return in
		}, None, nil},
		{"controller unreachable long enough", func() Input {
			in := input(nil)
			in.ControllerDownSince = now.Add(-16 * time.Second)
			return in
		}, Unknown, []string{ReasonControllerUnreachable}},
		{"container missing", func() Input { return input(nil) }, Down, []string{ReasonContainerMissing}},
		{"missing while provisioning", func() Input {
			in := input(nil)
			in.Lifecycle = lifecycle.Provisioning
			return in
		}, Starting, nil},
		{"restarting", func() Input {
			o := running(t, now)
			o.Container.State = "restarting"
			return input(o)
		}, Down, []string{ReasonContainerRestarting}},
		{"exited", func() Input {
			o := running(t, now)
			o.Container.State, o.Container.ExitCode = "exited", 1
			return input(o)
		}, Down, []string{ReasonContainerExited}},
		{"oom killed", func() Input {
			o := running(t, now)
			o.Container.State, o.Container.ExitCode, o.Container.OOMKilled = "exited", 137, true
			return input(o)
		}, Down, []string{ReasonContainerExited, ReasonOOMKilled}},
		{"created while provisioning", func() Input {
			o := running(t, now)
			o.Container.State = "created"
			in := input(o)
			in.Lifecycle = lifecycle.Provisioning
			return in
		}, Starting, nil},
		{"no telemetry, docker healthy", func() Input {
			o := running(t, now)
			o.Telemetry = controllerapi.Telemetry{}
			return input(o)
		}, Healthy, []string{ReasonNoTelemetry}},
		{"no telemetry, docker unhealthy", func() Input {
			o := running(t, now)
			o.Telemetry, o.Container.Health = controllerapi.Telemetry{}, "unhealthy"
			return input(o)
		}, Unresponsive, []string{ReasonNoTelemetry}},
		{"starting within the grace", func() Input {
			o := running(t, now)
			o.Telemetry = controllerapi.Telemetry{Configured: true, ConsecutiveFailures: 5}
			o.Container.StartedAt = ptr(now.Add(-30 * time.Second))
			return input(o)
		}, Starting, nil},
		{"never answered after the grace", func() Input {
			o := running(t, now)
			o.Telemetry = controllerapi.Telemetry{Configured: true, ConsecutiveFailures: 5, LastErrorCode: controllerapi.ScrapeTimeout}
			o.Container.StartedAt = ptr(now.Add(-2 * time.Minute))
			return input(o)
		}, Unresponsive, []string{controllerapi.ScrapeTimeout}},
		{"stopped answering", func() Input {
			o := running(t, now)
			o.Telemetry.OK, o.Telemetry.ConsecutiveFailures, o.Telemetry.LastErrorCode = false, 3, controllerapi.ScrapeUnsupportedSchema
			return input(o)
		}, Unresponsive, []string{controllerapi.ScrapeUnsupportedSchema}},
		{"two failures are not enough", func() Input {
			o := running(t, now)
			o.Telemetry.OK, o.Telemetry.ConsecutiveFailures = false, 2
			return input(o)
		}, Healthy, nil},
		{"a failure code is never empty", func() Input {
			o := running(t, now)
			o.Telemetry.OK, o.Telemetry.ConsecutiveFailures = false, 3
			return input(o)
		}, Unresponsive, []string{controllerapi.ScrapeUnreachable}},
		{"stuck", func() Input {
			o := running(t, now)
			o.Telemetry.Status.LastProgressAt = now.Add(-31 * time.Second)
			return input(o)
		}, Stuck, []string{ReasonNoProgress}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Evaluate(tt.in(), cfg)
			if r.Health != tt.health || !slices.Equal(r.Reasons, orEmpty(tt.reasons)) {
				t.Errorf("result = %s %v (%s), want %s %v", r.Health, r.Reasons, r.Message, tt.health, tt.reasons)
			}
			if r.Responsive != (r.Health == Healthy || r.Health == Degraded) {
				t.Errorf("responsive = %v for %s", r.Responsive, r.Health)
			}
		})
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestTheMessageOfStuckNamesThePhaseAndTheBudget(t *testing.T) {
	o := running(t, now)
	o.Telemetry.Status.Phase, o.Telemetry.Status.PhaseDetail, o.Telemetry.Status.PhaseBudgetSeconds = "PREPARING", "WORDLISTS", 1500
	o.Telemetry.Status.LastProgressAt = now.Add(-1602 * time.Second)
	r := Evaluate(input(o), cfg)
	want := "The main loop has not made progress for 1602 s in phase PREPARING (WORDLISTS); its budget is 1500 s."
	if r.Message != want {
		t.Errorf("message = %q\nwant      %q", r.Message, want)
	}
}

func TestExitCodeInTheMessage(t *testing.T) {
	o := running(t, now)
	o.Container.State, o.Container.ExitCode = "exited", 1
	if r := Evaluate(input(o), cfg); !strings.Contains(r.Message, "EXIT_CODE_1") {
		t.Errorf("message = %q", r.Message)
	}
}

func TestDegradations(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Input)
		reason string
	}{
		{"rabbitmq down", func(in *Input) { in.Observation.Telemetry.Status.Dependencies.RabbitMQ.Up = ptr(false) }, ReasonRabbitMQDown},
		{"core errors", func(in *Input) {
			// after the sample's last success (10:42:05): the calls fail since then
			in.Observation.Telemetry.Status.Dependencies.Core.LastErrorAt = ptr(now.Add(-time.Second))
		}, ReasonCoreErrors},
		{"object storage errors", func(in *Input) {
			in.Observation.Telemetry.Status.Dependencies.ObjectStorage.LastErrorAt = ptr(now.Add(-time.Minute))
		}, ReasonObjectStorageErrors},
		{"connector silent", func(in *Input) {
			in.Observation.Telemetry.Status.Job.Connector.LastMessageAt = ptr(now.Add(-16 * time.Second))
		}, ReasonConnectorSilent},
		{"memory near the limit", func(in *Input) { in.Observation.Resources.MemoryWorkingSetBytes = 3900000000 }, ReasonMemoryNearLimit},
		{"restarted recently", func(in *Input) { in.RestartedRecently = true }, ReasonRestartedRecently},
		{"slow status", func(in *Input) { in.Observation.Telemetry.LatencyMs = ptr(int64(1500)) }, ReasonSlowStatus},
		{"duplicate id", func(in *Input) { in.Observation.Flags = []string{controllerapi.FlagDuplicateID} }, ReasonDuplicateID},
		{"docker unhealthy", func(in *Input) { in.Observation.Container.Health = "unhealthy" }, ReasonDockerUnhealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input(running(t, now))
			tt.change(&in)
			// the second evaluation in a row shows the degradation
			first := Evaluate(in, cfg)
			in.Previous = first
			r := Evaluate(in, cfg)
			if r.Health != Degraded || !slices.Equal(r.Reasons, []string{tt.reason}) || !r.Responsive || r.Message == "" {
				t.Errorf("result = %s %v %q, want DEGRADED [%s]", r.Health, r.Reasons, r.Message, tt.reason)
			}
		})
	}
}

func TestOldOrRecoveredErrorsAreNoDegradation(t *testing.T) {
	in := input(running(t, now))
	core := &in.Observation.Telemetry.Status.Dependencies.Core
	core.LastErrorAt = ptr(now.Add(-time.Minute))
	core.LastSuccessAt = ptr(now.Add(-10 * time.Second))
	in.Observation.Telemetry.Status.Dependencies.ObjectStorage.LastErrorAt = ptr(now.Add(-301 * time.Second))
	in.Previous = Evaluate(in, cfg)
	if r := Evaluate(in, cfg); r.Health != Healthy {
		t.Errorf("result = %s %v, want HEALTHY: the core recovered, the storage error is old", r.Health, r.Reasons)
	}
}

func TestAConnectorIsSilentOnlyDuringAJob(t *testing.T) {
	in := input(running(t, now))
	in.Observation.Telemetry.Status.Phase = "FINISHING"
	in.Observation.Telemetry.Status.Job.Connector.LastMessageAt = ptr(now.Add(-time.Minute))
	in.Previous = Evaluate(in, cfg)
	if r := Evaluate(in, cfg); r.Health != Healthy {
		t.Errorf("result = %s %v, want HEALTHY outside RUNNING_JOB", r.Health, r.Reasons)
	}
}

func TestDegradedNeedsTwoPolls(t *testing.T) {
	in := input(running(t, now))
	healthy := Evaluate(in, cfg)

	in.Previous = healthy
	in.Observation.Telemetry.LatencyMs = ptr(int64(1500))
	first := Evaluate(in, cfg)
	if first.Health != Healthy || first.Candidate != Degraded {
		t.Fatalf("first slow poll = %s (candidate %s), want HEALTHY kept, candidate DEGRADED", first.Health, first.Candidate)
	}

	// one slow poll then a fast one: never shown
	in.Previous = first
	in.Observation.Telemetry.LatencyMs = ptr(int64(3))
	if r := Evaluate(in, cfg); r.Health != Healthy || r.Candidate != Healthy {
		t.Errorf("after a fast poll = %s (%s)", r.Health, r.Candidate)
	}

	in.Observation.Telemetry.LatencyMs = ptr(int64(1500))
	second := Evaluate(in, cfg)
	in.Previous = second
	if r := Evaluate(in, cfg); r.Health != Degraded {
		t.Errorf("two slow polls in a row = %s, want DEGRADED", r.Health)
	}
}

func TestOtherChangesAreApplied(t *testing.T) {
	in := input(running(t, now))
	in.Previous = Evaluate(in, cfg)
	in.Observation.Telemetry.Status.LastProgressAt = now.Add(-time.Minute)
	if r := Evaluate(in, cfg); r.Health != Stuck {
		t.Errorf("result = %s, want STUCK at once", r.Health)
	}
}

func TestTheFirstEvaluationShowsADegradationAtOnce(t *testing.T) {
	// after a restart of the control plane there is no previous result to wait for
	in := input(running(t, now))
	in.Observation.Telemetry.LatencyMs = ptr(int64(1500))
	if r := Evaluate(in, cfg); r.Health != Degraded {
		t.Errorf("result = %s, want DEGRADED", r.Health)
	}
}

func TestAShortControllerOutageKeepsTheHealth(t *testing.T) {
	in := input(running(t, now))
	healthy := Evaluate(in, cfg)
	in.Observation, in.Previous, in.ControllerDownSince = nil, healthy, now.Add(-10*time.Second)
	if r := Evaluate(in, cfg); r.Health != Healthy {
		t.Errorf("after 10 s without the controller = %s, want HEALTHY kept", r.Health)
	}
	in.Previous = Result{}
	if r := Evaluate(in, cfg); r.Health != Unknown {
		t.Errorf("without a previous result = %s, want UNKNOWN", r.Health)
	}
}

// the worker's clock may be ahead of or behind the control plane's: the progress age uses the document's own now
func TestClockSkew(t *testing.T) {
	for _, skew := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
		o := running(t, now.Add(skew))
		if r := Evaluate(input(o), cfg); r.Health != Healthy || *r.ProgressAge != 2*time.Second {
			t.Errorf("skew %s: %s, progress age %v", skew, r.Health, r.ProgressAge)
		}
	}
}

func TestSame(t *testing.T) {
	a := Result{Health: Degraded, Reasons: []string{ReasonCoreErrors}}
	if !Same(a, Result{Health: Degraded, Reasons: []string{ReasonCoreErrors}, Message: "other"}) {
		t.Error("same health and reasons are not Same")
	}
	if Same(a, Result{Health: Degraded, Reasons: []string{ReasonSlowStatus}}) || Same(a, Result{Health: Healthy}) ||
		Same(a, Result{Health: Degraded, Reasons: []string{ReasonCoreErrors, ReasonSlowStatus}}) {
		t.Error("different results are Same")
	}
}

func TestDescribe(t *testing.T) {
	got := Describe([]string{ReasonCoreErrors, "UNKNOWN_CODE", ReasonSlowStatus})
	if got != "The worker's calls to the core are failing. The worker answers its status slowly." {
		t.Errorf("Describe = %q", got)
	}
}

var _ = workerapi.PhaseIdle
