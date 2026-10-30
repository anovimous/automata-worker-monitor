// fakeworker stands in for the orchestrator of automata-worker in tests and UI work: it implements the telemetry
// contract (plan/05) with scripted scenarios and takes no real job.
package main

import (
	"context"
	log "log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/buildinfo"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// how often the fake main loop runs one step
const stepInterval = 250 * time.Millisecond

// the delay of GET /status in the slow-status scenario
const slowStatusDelay = 3 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("TELEMETRY_ADDR"), &http.Client{Timeout: 3 * time.Second}))
	}
	os.Exit(run())
}

// run returns the exit code, so that the deferred cleanup runs before the exit.
func run() int {
	cfg, err := loadConfig(os.Getenv, os.Hostname)
	if err != nil {
		log.Error("failed to load the configuration", "error", err)
		return 1
	}
	log.SetDefault(newLogger(cfg.LogFormat, cfg.WorkerID, os.Stderr))
	log.Info("fakeworker starting", "scenario", cfg.Scenario, "telemetryAddr", cfg.TelemetryAddr)

	now := func() time.Time { return time.Now().UTC() }
	f := newFake(cfg, now, rand.IntN, os.Getpid(), buildinfo.Version, buildinfo.Commit, runtime.Version())

	if cfg.TelemetryAddr != "" && cfg.Scenario != scenarioNoStatus {
		var delay time.Duration
		if cfg.Scenario == scenarioSlowStatus {
			delay = slowStatusDelay
		}
		srv := newServer(cfg.TelemetryAddr, f, delay)
		if err := srv.Start(); err != nil {
			log.Error("failed to start the telemetry server", "error", err)
			return 1
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			srv.Shutdown(ctx)
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR1, syscall.SIGUSR2)
	go handleSignals(ctx, sigs, f, os.Exit)

	running := func() bool { return f.phase() == workerapi.PhaseRunningJob }
	switch cfg.Scenario {
	case scenarioCPUHog:
		go burnCPU(ctx, running)
	case scenarioMemHog:
		go hogMemory(ctx, running, memHogTarget("/sys/fs/cgroup/memory.max"))
	}

	return loop(f, time.Tick(stepInterval))
}

// loop runs the fake main loop until a step says to exit.
func loop(f *fake, ticks <-chan time.Time) int {
	f.start()
	for range ticks {
		if exit, code := f.step(); exit {
			log.Info("exiting", "code", code)
			return code
		}
	}
	return 0
}
