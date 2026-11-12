package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

// testFake is a fake with a clock the test moves.
type testFake struct {
	*fake
	t     *testing.T
	clock time.Time
}

func newTestFake(t *testing.T, s scenario) *testFake {
	t.Helper()
	cfg := testConfig()
	cfg.Scenario = s
	tf := &testFake{t: t, clock: t0}
	tf.fake = newFake(cfg, func() time.Time { return tf.clock }, func(int) int { return 0 }, 7, "0.4.0", "1a4c23f", "go1.26.2")
	tf.start()
	return tf
}

func testConfig() config {
	return config{
		WorkerID:                 "w-host1-k3f9qa",
		TelemetryAddr:            defaultTelemetryAddr,
		LogFormat:                "text",
		Scenario:                 scenarioNormal,
		JobDuration:              20 * time.Second,
		IdleDuration:             4 * time.Second,
		Routines:                 []string{"fuzz", "masser"},
		HeartbeatIntervalSeconds: 5,
		HeartbeatTimeoutSeconds:  60,
		WordlistCacheDir:         "/var/cache/automata/wordlists",
		WordlistCacheMaxBytes:    1610612736,
	}
}

// advance moves the clock by d in steps of stepInterval and runs the loop at each; it fails the test when the
// loop exits.
func (tf *testFake) advance(d time.Duration) {
	tf.t.Helper()
	if exit, code := tf.advanceUntilExit(d); exit {
		tf.t.Fatalf("the loop exited with %d during %s, in phase %s", code, d, tf.phase())
	}
}

// advanceUntilExit is advance that stops when the loop exits.
func (tf *testFake) advanceUntilExit(d time.Duration) (exit bool, code int) {
	for end := tf.clock.Add(d); tf.clock.Before(end); {
		tf.clock = tf.clock.Add(stepInterval)
		if exit, code := tf.step(); exit {
			return true, code
		}
	}
	return false, 0
}

// until runs the loop until the phase is p, failing after limit.
func (tf *testFake) until(p workerapi.Phase, limit time.Duration) {
	tf.t.Helper()
	for end := tf.clock.Add(limit); tf.phase() != p; {
		if !tf.clock.Before(end) {
			tf.t.Fatalf("still in %s after %s, want %s", tf.phase(), limit, p)
		}
		tf.advance(stepInterval)
	}
}

func TestNewFakeIsStarting(t *testing.T) {
	f := newFake(testConfig(), func() time.Time { return t0 }, func(int) int { return 0 }, 7, "0.4.0", "1a4c23f", "go1.26.2")
	s := f.snapshot()
	if s.Phase != workerapi.PhaseStarting || s.PhaseBudgetSeconds != 60 || !s.StartedAt.Equal(t0) {
		t.Errorf("new fake in %s with budget %d since %s, want STARTING, 60, %s", s.Phase, s.PhaseBudgetSeconds, s.StartedAt, t0)
	}
	if s.SchemaVersion != 1 || s.WorkerID != "w-host1-k3f9qa" || s.PID != 7 || s.Version != "0.4.0" {
		t.Errorf("identity = %+v", s)
	}
}

func TestStartEntersIdle(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	if s := tf.snapshot(); s.Phase != workerapi.PhaseIdle || s.Job != nil || s.PhaseBudgetSeconds != 30 {
		t.Errorf("after start: %s with job %v and budget %d, want IDLE, no job, 30", s.Phase, s.Job, s.PhaseBudgetSeconds)
	}
}

func TestNormalCycleGoesThroughEveryPhase(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)

	var seen []workerapi.Phase
	for i := 0; i < 400 && len(seen) < 6; i++ {
		if p := tf.phase(); len(seen) == 0 || seen[len(seen)-1] != p {
			seen = append(seen, p)
		}
		tf.advance(stepInterval)
	}

	want := []workerapi.Phase{workerapi.PhaseIdle, workerapi.PhasePreparing, workerapi.PhaseStartingJob,
		workerapi.PhaseRunningJob, workerapi.PhaseFinishing, workerapi.PhaseIdle}
	if strings.Join(phases(seen), ",") != strings.Join(phases(want), ",") {
		t.Fatalf("phases = %v, want %v", seen, want)
	}

	s := tf.snapshot()
	c := s.Counters
	if c.MessagesTaken != 1 || c.JobsStarted != 1 || c.JobsByOutcome["FINISHED"] != 1 || s.Job != nil {
		t.Errorf("after one job: counters %+v, job %v, want 1 taken, 1 started, 1 FINISHED, no job", c, s.Job)
	}
	if s.WordlistCache.Files != 1 || s.WordlistCache.PinnedFiles != 0 {
		t.Errorf("wordlist cache = %+v, want 1 file, none pinned", s.WordlistCache)
	}
	if s.Dependencies.ObjectStorage.LastSuccessAt == nil || s.Dependencies.Core.LastSuccessAt == nil {
		t.Errorf("dependencies = %+v, want successes for core and object storage", s.Dependencies)
	}
}

func phases(ps []workerapi.Phase) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

func TestJobIDsIncrementFrom1000(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhasePreparing, time.Minute)
	if id := tf.snapshot().Job.JobID; id != 1000 {
		t.Errorf("first job id = %d, want 1000", id)
	}
	tf.until(workerapi.PhaseIdle, time.Minute)
	tf.until(workerapi.PhasePreparing, time.Minute)
	if id := tf.snapshot().Job.JobID; id != 1001 {
		t.Errorf("second job id = %d, want 1001", id)
	}
}

func TestPreparingShowsItsSubSteps(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhasePreparing, time.Minute)
	details := map[string]bool{}
	for tf.phase() == workerapi.PhasePreparing {
		details[tf.snapshot().PhaseDetail] = true
		tf.advance(stepInterval)
	}
	for _, d := range preparingDetails {
		if !details[d] {
			t.Errorf("detail %s never shown, saw %v", d, details)
		}
	}
}

func TestStartingJobSetsTheCommandAndConnector(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhaseStartingJob, time.Minute)
	s := tf.snapshot()
	if s.Job.Command != "ffuf-connector" || s.Job.RoutineKey != "fuzz" || s.PhaseDetail != "STARTING_CONNECTOR" {
		t.Errorf("job = %+v in %s, want fuzz with ffuf-connector, STARTING_CONNECTOR", s.Job, s.PhaseDetail)
	}
	if c := s.Job.Connector; c == nil || c.Connected || c.HeartbeatIntervalSeconds != 5 {
		t.Errorf("connector = %+v, want not yet connected, interval 5", c)
	}
	if s.Job.State != "" || s.Job.StartedAt != nil {
		t.Errorf("job state %q started %v, want empty before RUNNING", s.Job.State, s.Job.StartedAt)
	}
}

func TestUnknownRoutineGetsAConnectorName(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.cfg.Routines = []string{"probe"}
	tf.until(workerapi.PhaseStartingJob, time.Minute)
	if cmd := tf.snapshot().Job.Command; cmd != "probe-connector" {
		t.Errorf("command = %q, want probe-connector", cmd)
	}
}

func TestRunningJobHeartbeatsAndPollsTheCore(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	tf.advance(12 * time.Second)

	s := tf.snapshot()
	if s.Job.State != "RUNNING" || s.Job.StartedAt == nil || !s.Job.Connector.Connected || s.Counters.JobsStarted != 1 {
		t.Fatalf("job = %+v, want RUNNING, started, connected", s.Job)
	}
	if age := s.Now.Sub(*s.Job.Connector.LastHeartbeatAt); age > 5*time.Second {
		t.Errorf("heartbeat age = %s, want ≤ 5s", age)
	}
	if age := s.Now.Sub(*s.Dependencies.Core.LastSuccessAt); age > corePollInterval {
		t.Errorf("last core success %s ago, want ≤ %s", age, corePollInterval)
	}
	if s.WordlistCache.PinnedFiles != 1 {
		t.Errorf("pinned files = %d, want 1 during a job", s.WordlistCache.PinnedFiles)
	}
}

func TestProgressIsTouchedEveryStep(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	for i := 0; i < 200; i++ {
		tf.advance(stepInterval)
		if ok, age, phase := tf.health(); !ok || age != 0 {
			t.Fatalf("health in %s = %v with age %s, want ok and 0", phase, ok, age)
		}
	}
}

func TestFinishingReportsAfterTheUpload(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhaseFinishing, time.Minute)
	if d := tf.snapshot().PhaseDetail; d != "UPLOADING_RESULT" {
		t.Errorf("detail = %s, want UPLOADING_RESULT", d)
	}
	tf.advance(finishingDuration / 2)
	if d := tf.snapshot().PhaseDetail; d != "REPORTING" {
		t.Errorf("detail = %s, want REPORTING", d)
	}
}

func TestStatusIsAValidDocumentInEveryPhase(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	for i := 0; i < 160; i++ {
		b, err := json.Marshal(tf.snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := workerapi.Parse(b); err != nil {
			t.Fatalf("Parse in %s: %v", tf.phase(), err)
		}
		var s workerapi.Status
		contracttest.DecodeStrict(t, b, &s)
		tf.advance(stepInterval)
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	s := tf.snapshot()
	s.Job.JobID = 1
	s.Job.Connector.Connected = false
	s.Counters.JobsByOutcome["X"] = 9
	s.WordlistCache.Files = 99

	again := tf.snapshot()
	if again.Job.JobID != 1000 || !again.Job.Connector.Connected || again.Counters.JobsByOutcome["X"] != 0 || again.WordlistCache.Files == 99 {
		t.Errorf("changing a snapshot changed the fake: %+v", again)
	}
}

func TestDrainWhileIdleExitsWithZero(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.setDraining(true)
	exit, code := tf.advanceUntilExit(stepInterval)
	if !exit || code != 0 {
		t.Errorf("exit, code = %v, %d, want true, 0", exit, code)
	}
	if s := tf.snapshot(); s.Phase != workerapi.PhaseDrained || !s.Draining {
		t.Errorf("status = %s draining %v, want DRAINED and draining", s.Phase, s.Draining)
	}
}

func TestDrainDuringAJobExitsAfterIt(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	tf.setDraining(true)

	exit, code := tf.advanceUntilExit(time.Minute)
	if !exit || code != 0 {
		t.Fatalf("exit, code = %v, %d, want true, 0", exit, code)
	}
	s := tf.snapshot()
	if s.Counters.JobsByOutcome["FINISHED"] != 1 || s.Counters.MessagesTaken != 1 {
		t.Errorf("counters = %+v, want the job finished and no new message taken", s.Counters)
	}
}

func TestUndrainCancelsADrain(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	tf.setDraining(true)
	tf.setDraining(false)
	tf.until(workerapi.PhaseIdle, time.Minute)
	tf.until(workerapi.PhasePreparing, time.Minute)
	if s := tf.snapshot(); s.Draining || s.Counters.MessagesTaken != 2 {
		t.Errorf("draining %v with %d messages, want a second message taken", s.Draining, s.Counters.MessagesTaken)
	}
}

func TestUndrainIsIgnoredOnceShuttingDown(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.setDraining(true)
	tf.shutdown()
	tf.setDraining(false)
	if !tf.snapshot().Draining {
		t.Error("undrain was applied after the shutdown started")
	}
}

func TestShutdownWhileIdleExitsWithZero(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	if !tf.shutdown() {
		t.Fatal("first shutdown returned false")
	}
	if exit, code := tf.advanceUntilExit(stepInterval); !exit || code != 0 {
		t.Errorf("exit, code = %v, %d, want true, 0", exit, code)
	}
}

func TestSecondShutdownIsReported(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.shutdown()
	if tf.shutdown() {
		t.Error("second shutdown returned true")
	}
}

func TestShutdownWhilePreparingRequeues(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhasePreparing, time.Minute)
	tf.shutdown()
	exit, code := tf.advanceUntilExit(stepInterval)
	if !exit || code != 0 {
		t.Fatalf("exit, code = %v, %d, want true, 0", exit, code)
	}
	s := tf.snapshot()
	if s.Counters.JobsByOutcome["NOT_STARTED"] != 1 || s.Job != nil || s.Dependencies.Core.LastSuccessAt != nil {
		t.Errorf("counters %+v, job %v, core %+v: want NOT_STARTED, no job, nothing reported", s.Counters, s.Job, s.Dependencies.Core)
	}
}

func TestShutdownDuringAJobReportsSelfTerminated(t *testing.T) {
	for _, p := range []workerapi.Phase{workerapi.PhaseStartingJob, workerapi.PhaseRunningJob} {
		t.Run(string(p), func(t *testing.T) {
			tf := newTestFake(t, scenarioNormal)
			tf.until(p, time.Minute)
			tf.shutdown()
			tf.advance(stepInterval)
			if s := tf.snapshot(); s.Phase != workerapi.PhaseShuttingDown || !s.ShuttingDown || s.PhaseBudgetSeconds != 90 {
				t.Fatalf("status = %s shuttingDown %v budget %d, want SHUTTING_DOWN, true, 90", s.Phase, s.ShuttingDown, s.PhaseBudgetSeconds)
			}

			exit, code := tf.advanceUntilExit(shutdownReport + stepInterval)
			if !exit || code != 0 {
				t.Fatalf("exit, code = %v, %d, want true, 0 after the report", exit, code)
			}
			if n := tf.snapshot().Counters.JobsByOutcome["SELFTERMINATED"]; n != 1 {
				t.Errorf("SELFTERMINATED = %d, want 1", n)
			}
		})
	}
}

func TestShutdownWhileFinishingLetsTheJobFinish(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.until(workerapi.PhaseFinishing, time.Minute)
	tf.shutdown()
	exit, code := tf.advanceUntilExit(finishingDuration + stepInterval)
	if !exit || code != 0 {
		t.Fatalf("exit, code = %v, %d, want true, 0", exit, code)
	}
	if n := tf.snapshot().Counters.JobsByOutcome["FINISHED"]; n != 1 {
		t.Errorf("FINISHED = %d, want 1", n)
	}
}

func TestStuckStopsTouchingProgress(t *testing.T) {
	tf := newTestFake(t, scenarioStuck)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	tf.advance(2 * tf.cfg.JobDuration)

	ok, age, phase := tf.health()
	if ok || phase != workerapi.PhaseRunningJob || age < 30*time.Second {
		t.Errorf("health = %v, age %s in %s, want not ok, > 30s in RUNNING_JOB", ok, age, phase)
	}
	if s := tf.snapshot(); s.ProgressAge() != age {
		t.Errorf("status progress age = %s, want %s", s.ProgressAge(), age)
	}
}

func TestStuckStillShutsDown(t *testing.T) {
	tf := newTestFake(t, scenarioStuck)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	tf.advance(time.Minute)
	tf.shutdown()
	if exit, code := tf.advanceUntilExit(shutdownReport + time.Second); !exit || code != 0 {
		t.Errorf("exit, code = %v, %d, want true, 0", exit, code)
	}
}

func TestCrashExitsWithOneAfter30Seconds(t *testing.T) {
	tf := newTestFake(t, scenarioCrash)
	tf.advance(crashAfter - stepInterval)
	if exit, code := tf.advanceUntilExit(stepInterval); !exit || code != 1 {
		t.Errorf("exit, code = %v, %d, want true, 1", exit, code)
	}
}

func TestRabbitDownTakesNoJob(t *testing.T) {
	tf := newTestFake(t, scenarioRabbitDown)
	tf.advance(time.Minute)
	s := tf.snapshot()
	r := s.Dependencies.RabbitMQ
	if r.Up == nil || *r.Up || r.ErrorsTotal == 0 || r.LastError == "" || r.LastSuccessAt != nil {
		t.Errorf("rabbitmq = %+v, want down with errors", r)
	}
	if s.Phase != workerapi.PhaseIdle || s.Counters.MessagesTaken != 0 {
		t.Errorf("%s with %d messages, want IDLE and none", s.Phase, s.Counters.MessagesTaken)
	}
}

func TestNormalRabbitIsUp(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.advance(time.Second)
	if r := tf.snapshot().Dependencies.RabbitMQ; r.Up == nil || !*r.Up || r.LastSuccessAt == nil {
		t.Errorf("rabbitmq = %+v, want up with a success", r)
	}
}

func TestCoreErrorsAreRecordedRedacted(t *testing.T) {
	tf := newTestFake(t, scenarioCoreErrors)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	tf.advance(11 * time.Second)
	c := tf.snapshot().Dependencies.Core
	if c.ErrorsTotal < 2 || c.LastErrorAt == nil || c.LastSuccessAt != nil {
		t.Errorf("core = %+v, want ≥ 2 errors and no success", c)
	}
	if !strings.Contains(c.LastError, "***@") {
		t.Errorf("lastError = %q, want the user info masked", c.LastError)
	}
}

func TestConnectorSilentSendsNoMessage(t *testing.T) {
	tf := newTestFake(t, scenarioConnectorSilent)
	tf.until(workerapi.PhaseRunningJob, time.Minute)
	connectedAt := *tf.snapshot().Job.Connector.LastMessageAt
	tf.advance(15 * time.Second)
	if last := *tf.snapshot().Job.Connector.LastMessageAt; !last.Equal(connectedAt) {
		t.Errorf("last message at %s, want %s (no message since it connected)", last, connectedAt)
	}
}

func TestHealthComparesTheAgeWithTheBudget(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	tf.clock = tf.clock.Add(30 * time.Second)
	if ok, age, _ := tf.health(); !ok || age != 30*time.Second {
		t.Errorf("health at the budget = %v, %s, want ok, 30s", ok, age)
	}
	tf.clock = tf.clock.Add(time.Second)
	if ok, _, phase := tf.health(); ok || phase != workerapi.PhaseIdle {
		t.Errorf("health past the budget = %v in %s, want not ok in IDLE", ok, phase)
	}
}

func TestConcurrentReadsWhileTheLoopRuns(t *testing.T) {
	var mu sync.Mutex
	clock := t0
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	f := newFake(testConfig(), now, func(int) int { return 1 }, 7, "dev", "unknown", "go")
	f.start()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				f.snapshot()
				f.health()
				f.setDraining(false)
			}
		})
	}
	for range 400 {
		mu.Lock()
		clock = clock.Add(stepInterval)
		mu.Unlock()
		f.step()
	}
	wg.Wait()
}
