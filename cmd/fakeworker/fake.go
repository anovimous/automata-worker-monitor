package main

import (
	"fmt"
	log "log/slog"
	"maps"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// How long the scripted phases take (FAKE_JOB_SECONDS and FAKE_IDLE_SECONDS set the others).
const (
	preparingDuration   = 3 * time.Second
	startingJobDuration = 1 * time.Second
	finishingDuration   = 2 * time.Second
	shutdownReport      = 2 * time.Second // the fake SELFTERMINATED report on SIGTERM
	crashAfter          = 30 * time.Second

	queuePollInterval = 2 * time.Second // like the worker's idle wait
	corePollInterval  = 5 * time.Second // like CENTRAL_POLL_INTERVAL_SECONDS
	firstJobID        = 1000
	fakeWordlistBytes = 64 << 20
	maxWordlistFiles  = 4
)

// phaseBudgets are the worker's budgets. The fake reports them like the worker does; the monitor
// never hard-codes them.
var phaseBudgets = map[workerapi.Phase]int{
	workerapi.PhaseStarting:     60,
	workerapi.PhaseIdle:         30,
	workerapi.PhasePreparing:    1500,
	workerapi.PhaseStartingJob:  60,
	workerapi.PhaseRunningJob:   30,
	workerapi.PhaseFinishing:    900,
	workerapi.PhaseDrained:      30,
	workerapi.PhaseShuttingDown: 90,
}

// the sub-steps of PREPARING, shown one after the other
var preparingDetails = []string{"CREATING_WORKDIR", "DOWNLOADING_FILES", "INITIAL_POLL", "WORDLISTS", "MAPPING"}

// the connector of each routine, as in the worker's routine-worker-mapping.json
var routineCommands = map[string]string{"fuzz": "ffuf-connector", "masser": "masser-connector"}

// fake plays the orchestrator's main loop from a script. step is its loop; the HTTP server and the signal
// handler read and change it concurrently, hence the mutex.
type fake struct {
	mu   sync.Mutex
	cfg  config
	now  func() time.Time
	pick func(n int) int // picks the routine of a job

	status        workerapi.Status
	phaseEnds     time.Time
	nextJobID     int64
	lastQueuePoll time.Time
	lastCorePoll  time.Time
	stuck         bool
}

func newFake(cfg config, now func() time.Time, pick func(n int) int, pid int, version, commit, goVersion string) *fake {
	f := &fake{cfg: cfg, now: now, pick: pick, nextJobID: firstJobID}
	t := now()
	f.status = workerapi.Status{
		SchemaVersion: workerapi.SchemaVersion,
		WorkerID:      cfg.WorkerID,
		Version:       version,
		Commit:        commit,
		GoVersion:     goVersion,
		PID:           pid,
		StartedAt:     t,
		Counters:      workerapi.Counters{JobsByOutcome: map[string]int64{}},
		WordlistCache: &workerapi.WordlistCache{Dir: cfg.WordlistCacheDir, MaxBytes: cfg.WordlistCacheMaxBytes},
	}
	f.setPhase(workerapi.PhaseStarting, "", t)
	if cfg.Scenario == scenarioRabbitDown {
		f.status.Dependencies.RabbitMQ.Up = boolPtr(false)
	}
	return f
}

// start is the beginning of the main loop.
func (f *fake) start() {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	f.setPhase(workerapi.PhaseIdle, "", now)
	f.phaseEnds = now.Add(f.cfg.IdleDuration)
}

// step is one iteration of the loop. It returns true with the exit code when the process must exit.
func (f *fake) step() (exit bool, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()

	if f.cfg.Scenario == scenarioCrash && now.Sub(f.status.StartedAt) >= crashAfter {
		log.Error("crash scenario: exiting with 1")
		return true, 1
	}
	// a hung loop does nothing at all, but a shutdown still ends it (the real loop selects on it)
	if f.stuck && !f.status.ShuttingDown {
		return false, 0
	}

	switch f.status.Phase {
	case workerapi.PhaseIdle:
		if f.status.ShuttingDown {
			return true, 0
		}
		if f.status.Draining {
			f.setPhase(workerapi.PhaseDrained, "", now)
			log.Info("drained, exiting")
			return true, 0
		}
		if now.Sub(f.lastQueuePoll) >= queuePollInterval {
			f.lastQueuePoll = now
			if f.cfg.Scenario == scenarioRabbitDown {
				f.dependencyResult(&f.status.Dependencies.RabbitMQ, now, fmt.Errorf("Exception (504) Reason: \"channel/connection is not open\""))
			} else {
				f.dependencyResult(&f.status.Dependencies.RabbitMQ, now, nil)
				f.status.Dependencies.RabbitMQ.Up = boolPtr(true)
			}
		}
		if f.cfg.Scenario != scenarioRabbitDown && !now.Before(f.phaseEnds) {
			f.takeMessage(now)
		}

	case workerapi.PhasePreparing:
		if f.status.ShuttingDown {
			// the message is not acked yet: it goes back to the queue, nothing is reported
			log.Info("shutting down while preparing, the message goes back to the queue", "jobId", f.status.Job.JobID)
			f.jobEnded("NOT_STARTED")
			return true, 0
		}
		if now.Before(f.phaseEnds) {
			i := int(now.Sub(f.status.PhaseSince) * time.Duration(len(preparingDetails)) / preparingDuration)
			f.status.PhaseDetail = preparingDetails[min(i, len(preparingDetails)-1)]
			break
		}
		f.dependencyResult(&f.status.Dependencies.ObjectStorage, now, nil)
		f.corePoll(now)
		f.setPhase(workerapi.PhaseStartingJob, "STARTING_CONNECTOR", now)
		f.phaseEnds = now.Add(startingJobDuration)
		f.status.Job.Command = routineCommands[f.status.Job.RoutineKey]
		if f.status.Job.Command == "" {
			f.status.Job.Command = f.status.Job.RoutineKey + "-connector"
		}
		f.status.Job.Connector = &workerapi.Connector{
			HeartbeatIntervalSeconds: f.cfg.HeartbeatIntervalSeconds,
			HeartbeatTimeoutSeconds:  f.cfg.HeartbeatTimeoutSeconds,
		}
		f.status.WordlistCache.PinnedFiles = 1

	case workerapi.PhaseStartingJob:
		if f.status.ShuttingDown {
			f.beginShutdownReport(now)
			break
		}
		if now.Before(f.phaseEnds) {
			break
		}
		f.setPhase(workerapi.PhaseRunningJob, "", now)
		f.phaseEnds = now.Add(f.cfg.JobDuration)
		f.lastCorePoll = now
		job := f.status.Job
		job.State = "RUNNING"
		job.StartedAt = timePtr(now)
		job.Connector.Connected = true
		job.Connector.LastMessageAt = timePtr(now)
		job.Connector.LastHeartbeatAt = timePtr(now)
		f.status.Counters.JobsStarted++
		log.Info("job started", "jobId", job.JobID, "routineKey", job.RoutineKey)
		if f.cfg.Scenario == scenarioStuck {
			log.Info("stuck scenario: the loop stops here")
			f.stuck = true
			return false, 0
		}

	case workerapi.PhaseRunningJob:
		if f.status.ShuttingDown {
			f.beginShutdownReport(now)
			break
		}
		c := f.status.Job.Connector
		if f.cfg.Scenario != scenarioConnectorSilent && now.Sub(*c.LastHeartbeatAt) >= time.Duration(c.HeartbeatIntervalSeconds)*time.Second {
			c.LastMessageAt = timePtr(now)
			c.LastHeartbeatAt = timePtr(now)
		}
		if now.Sub(f.lastCorePoll) >= corePollInterval {
			f.lastCorePoll = now
			f.corePoll(now)
		}
		if !now.Before(f.phaseEnds) {
			f.setPhase(workerapi.PhaseFinishing, "UPLOADING_RESULT", now)
			f.phaseEnds = now.Add(finishingDuration)
		}

	case workerapi.PhaseFinishing:
		// not cut by a shutdown: the step holds the job's real outcome
		if now.Before(f.phaseEnds) {
			if now.Sub(f.status.PhaseSince) >= finishingDuration/2 {
				f.status.PhaseDetail = "REPORTING"
			}
			break
		}
		f.dependencyResult(&f.status.Dependencies.ObjectStorage, now, nil)
		f.corePoll(now)
		log.Info("job ended", "jobId", f.status.Job.JobID, "state", "FINISHED")
		f.jobEnded("FINISHED")
		if f.status.ShuttingDown {
			return true, 0
		}
		f.setPhase(workerapi.PhaseIdle, "", now)
		f.phaseEnds = now.Add(f.cfg.IdleDuration)

	case workerapi.PhaseShuttingDown:
		if now.Before(f.phaseEnds) {
			break
		}
		f.corePoll(now)
		log.Info("job ended", "jobId", f.status.Job.JobID, "state", "SELFTERMINATED")
		f.jobEnded("SELFTERMINATED")
		return true, 0
	}

	f.touch(now)
	return false, 0
}

// setDraining is SIGUSR1 (true) and SIGUSR2 (false); an undrain is ignored once the shutdown started.
func (f *fake) setDraining(draining bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !draining && f.status.ShuttingDown {
		return
	}
	if f.status.Draining != draining {
		log.Info("drain changed", "draining", draining)
	}
	f.status.Draining = draining
}

// shutdown is SIGTERM/SIGINT. It returns false when a shutdown was already under way: a second signal means
// exit at once.
func (f *fake) shutdown() (first bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status.ShuttingDown {
		return false
	}
	log.Info("shutting down", "phase", f.status.Phase)
	f.status.ShuttingDown = true
	return true
}

// snapshot is the status document as of now.
func (f *fake) snapshot() workerapi.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.status
	s.Now = f.now()
	s.Counters.JobsByOutcome = maps.Clone(f.status.Counters.JobsByOutcome)
	if f.status.Job != nil {
		job := *f.status.Job
		if job.Connector != nil {
			c := *job.Connector
			job.Connector = &c
		}
		s.Job = &job
	}
	if f.status.WordlistCache != nil {
		c := *f.status.WordlistCache
		s.WordlistCache = &c
	}
	return s
}

// health is what GET /healthz answers: the loop made progress within the budget of its phase.
func (f *fake) health() (ok bool, progressAge time.Duration, phase workerapi.Phase) {
	f.mu.Lock()
	defer f.mu.Unlock()
	age := f.now().Sub(f.status.LastProgressAt)
	return age <= time.Duration(f.status.PhaseBudgetSeconds)*time.Second, age, f.status.Phase
}

// phase is the current phase, for the hog goroutines.
func (f *fake) phase() workerapi.Phase {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status.Phase
}

// The helpers below are called with f.mu held.

func (f *fake) setPhase(p workerapi.Phase, detail string, now time.Time) {
	f.status.Phase = p
	f.status.PhaseDetail = detail
	f.status.PhaseSince = now
	f.status.PhaseBudgetSeconds = phaseBudgets[p]
	f.touch(now)
}

func (f *fake) touch(now time.Time) {
	f.status.LastProgressAt = now
}

func (f *fake) takeMessage(now time.Time) {
	routine := f.cfg.Routines[f.pick(len(f.cfg.Routines))]
	f.status.Job = &workerapi.Job{JobID: f.nextJobID, RoutineKey: routine, TakenAt: now}
	f.nextJobID++
	f.status.Counters.MessagesTaken++
	f.setPhase(workerapi.PhasePreparing, preparingDetails[0], now)
	f.phaseEnds = now.Add(preparingDuration)
	log.Info("message taken", "jobId", f.status.Job.JobID, "routineKey", routine)
}

// beginShutdownReport ends a started job like the worker on SIGTERM: terminate the tool, then report
// SELFTERMINATED, which takes shutdownReport here.
func (f *fake) beginShutdownReport(now time.Time) {
	f.setPhase(workerapi.PhaseShuttingDown, "REPORTING", now)
	f.phaseEnds = now.Add(shutdownReport)
}

func (f *fake) corePoll(now time.Time) {
	var err error
	if f.cfg.Scenario == scenarioCoreErrors {
		err = fmt.Errorf("failed to poll central service for job %d: Get \"http://***@core:8080/api/external/jobs/http/poll/%d\": dial tcp 172.20.0.3:8080: connect: connection refused",
			f.status.Job.JobID, f.status.Job.JobID)
	}
	f.dependencyResult(&f.status.Dependencies.Core, now, err)
}

func (f *fake) dependencyResult(d *workerapi.Dependency, now time.Time, err error) {
	if err == nil {
		d.LastSuccessAt = timePtr(now)
		return
	}
	d.LastErrorAt = timePtr(now)
	d.LastError = err.Error()
	d.ErrorsTotal++
}

// jobEnded counts the outcome and clears the job; a job that went as far as the connector filled the cache.
func (f *fake) jobEnded(outcome string) {
	f.status.Counters.JobsByOutcome[outcome]++
	if f.status.Job != nil && f.status.Job.Connector != nil && f.status.WordlistCache.Files < maxWordlistFiles {
		f.status.WordlistCache.Files++
		f.status.WordlistCache.Bytes += fakeWordlistBytes
	}
	f.status.WordlistCache.PinnedFiles = 0
	f.status.Job = nil
}

func boolPtr(b bool) *bool { return &b }

func timePtr(t time.Time) *time.Time { return &t }
