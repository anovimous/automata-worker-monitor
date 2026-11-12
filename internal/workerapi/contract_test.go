package workerapi

import (
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
)

// The samples belong to the worker repo (contract/telemetry/); contracts/ holds an equal copy.

func TestSamplesRoundTripThroughStatus(t *testing.T) {
	for _, name := range []string{"worker-status.v1.running.json", "worker-status.v1.idle.json"} {
		t.Run(name, func(t *testing.T) {
			var s Status
			contracttest.AssertRoundTrip(t, name, &s)
		})
	}
}

func TestRunningSampleIsParsed(t *testing.T) {
	s, err := Parse(contracttest.Read(t, "worker-status.v1.running.json"))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}

	if s.WorkerID != "w-host1-k3f9qa" || s.Phase != PhaseRunningJob || s.PhaseBudgetSeconds != 30 {
		t.Errorf("status = %s in %s with budget %d, want w-host1-k3f9qa in RUNNING_JOB with budget 30",
			s.WorkerID, s.Phase, s.PhaseBudgetSeconds)
	}
	if s.Job == nil || s.Job.JobID != 4211 || s.Job.RoutineKey != "fuzz" || s.Job.State != "RUNNING" {
		t.Fatalf("job = %+v, want job 4211 of routine fuzz, RUNNING", s.Job)
	}
	if s.Job.Connector == nil || !s.Job.Connector.Connected || s.Job.Connector.HeartbeatIntervalSeconds != 5 {
		t.Errorf("connector = %+v, want connected with a 5 s heartbeat", s.Job.Connector)
	}
	if r := s.Job.Resources; r == nil || r.Job == nil || r.Job.MemoryBytes != nil || r.Job.CPUUsageSeconds != 812.41 {
		t.Errorf("job resources = %+v, want 812.41 s of CPU and no memory", r)
	}
	if up := s.Dependencies.RabbitMQ.Up; up == nil || !*up {
		t.Errorf("rabbitmq up = %v, want true", up)
	}
	if s.Dependencies.Core.Up != nil || s.Dependencies.Core.ErrorsTotal != 3 {
		t.Errorf("core = %+v, want up null and 3 errors", s.Dependencies.Core)
	}
	if s.WordlistCache == nil || s.WordlistCache.Files != 4 {
		t.Errorf("wordlist cache = %+v, want 4 files", s.WordlistCache)
	}
	if s.Counters.JobsByOutcome["FINISHED"] != 30 {
		t.Errorf("jobs by outcome = %v, want 30 FINISHED", s.Counters.JobsByOutcome)
	}
	if got := s.ProgressAge(); got != 2*time.Second {
		t.Errorf("ProgressAge() = %s, want 2s", got)
	}
}

func TestIdleSampleHasNoJob(t *testing.T) {
	s, err := Parse(contracttest.Read(t, "worker-status.v1.idle.json"))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}

	if s.Phase != PhaseIdle || s.Job != nil || s.WordlistCache != nil {
		t.Errorf("status = %s, job %v, cache %v, want IDLE with no job and no cache", s.Phase, s.Job, s.WordlistCache)
	}
	if s.Counters.JobsByOutcome == nil {
		t.Error("jobsByOutcome is nil, want an empty map so that it is written as {}")
	}
}
