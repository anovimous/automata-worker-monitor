// worker-machine-controller runs on every Docker host of the workers: it creates and controls the worker containers,
// watches them (Docker state, stats, the workers' own status) and serves all of it to the workers control plane
// (plan/06-worker-machine-controller.md). It keeps no state: Docker is the truth.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	log "log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/buildinfo"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/api"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/hostmetrics"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/ports"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/scrape"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/stats"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/volumes"
)

// the oldest Docker Engine API the controller works with
const minAPIVersion = "1.44"

// how long in-flight requests have to finish on SIGTERM
const shutdownTimeout = 10 * time.Second

func main() {
	os.Exit(run())
}

// run returns the exit code, so that the deferred cleanup runs first.
func run() int {
	cfg, err := config.Load(os.Getenv, os.Hostname)
	if err != nil {
		log.Error("failed to load the configuration", "error", err)
		return 1
	}
	log.SetDefault(newLogger(cfg.LogFormat, cfg.HostName, os.Stderr))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	engine, err := docker.New(cfg.DockerHost)
	if err != nil {
		log.Error("failed to create the Docker client", "error", err)
		return 1
	}
	defer engine.Close()
	info, err := checkDocker(ctx, engine)
	if err != nil {
		log.Error("Docker is not usable", "error", err)
		return 1
	}
	if _, err := os.ReadDir(cfg.EnvDir); err != nil {
		log.Error("the env directory can not be read", "dir", cfg.EnvDir, "error", err)
		return 1
	}

	now := func() time.Time { return time.Now().UTC() }
	disc := discovery.New(engine)
	statsManager := stats.NewManager(engine)
	defer statsManager.Stop()
	scraper := scrape.New(&http.Client{Timeout: scrape.Timeout}, now)
	host := hostmetrics.New(hostmetrics.Gopsutil{}, cfg.DiskPaths, info.ServerVersion, now)
	vols := volumes.New(engine, now)
	prov := provision.New(engine, cfg, ports.New(cfg.PortRange), disc, now)

	go disc.Run(ctx, cfg.DiscoveryInterval)
	go host.Run(ctx, cfg.ScrapeInterval)
	go vols.Run(ctx, volumes.Interval)
	go observeEvery(ctx, cfg.ScrapeInterval, disc, statsManager, scraper)

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: api.NewHandler(api.Deps{
			HostName: cfg.HostName, Version: buildinfo.Version, Token: cfg.Token, EnvDir: cfg.EnvDir,
			DockerVersion: info.ServerVersion, DockerAPIVersion: info.APIVersion,
			Engine: engine, Workers: disc, Stats: statsManager, Scrapes: scraper, Volumes: vols, Host: host,
			Provisioner: prov, Now: now,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// no write timeout: a stop waits up to 600 s and a log stream follows as long as it is read
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("worker machine controller listening", "addr", cfg.ListenAddr, "tls", cfg.TLS(),
			"version", buildinfo.Version, "docker", info.ServerVersion)
		if cfg.TLS() {
			serveErr <- srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			serveErr <- srv.ListenAndServe()
		}
	}()

	select {
	case err := <-serveErr:
		log.Error("the API server stopped", "error", err)
		return 1
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("requests were still running at the shutdown", "error", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("the API server stopped", "error", err)
		return 1
	}
	return 0
}

// dockerChecker is what checkDocker needs of the engine.
type dockerChecker interface {
	Ping(ctx context.Context) (string, error)
	Info(ctx context.Context) (docker.Info, error)
}

// checkDocker makes sure the daemon answers with an API version the controller works with.
func checkDocker(ctx context.Context, engine dockerChecker) (docker.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	version, err := engine.Ping(ctx)
	if err != nil {
		return docker.Info{}, err
	}
	if !apiVersionAtLeast(version, minAPIVersion) {
		return docker.Info{}, fmt.Errorf("the Docker Engine API is %s, at least %s is needed", version, minAPIVersion)
	}
	info, err := engine.Info(ctx)
	if err != nil {
		return docker.Info{}, err
	}
	if info.APIVersion == "" {
		info.APIVersion = version
	}
	return info, nil
}

// apiVersionAtLeast compares two Docker API versions (major.minor).
func apiVersionAtLeast(version, min string) bool {
	parse := func(v string) (int, int, bool) {
		major, minor, ok := strings.Cut(v, ".")
		a, err1 := strconv.Atoi(major)
		b, err2 := strconv.Atoi(minor)
		return a, b, ok && err1 == nil && err2 == nil
	}
	va, vb, ok1 := parse(version)
	ma, mb, ok2 := parse(min)
	if !ok1 || !ok2 {
		return false
	}
	return va > ma || (va == ma && vb >= mb)
}

// syncer and scraper are what observe drives (stats.Manager, scrape.Scraper).
type syncer interface {
	Sync(ctx context.Context, running []string)
}

type scraper interface {
	ScrapeAll(ctx context.Context, targets []scrape.Target)
}

type workerLister interface {
	Workers() []discovery.Container
}

// observe gives the stats streams the running containers and scrapes the status of those with a telemetry URL.
func observe(ctx context.Context, workers workerLister, s syncer, sc scraper) {
	var running []string
	var targets []scrape.Target
	for _, c := range workers.Workers() {
		if !c.Running() {
			continue
		}
		running = append(running, c.ID)
		if c.TelemetryURL != "" {
			targets = append(targets, scrape.Target{ContainerID: c.ID, URL: c.TelemetryURL})
		}
	}
	s.Sync(ctx, running)
	sc.ScrapeAll(ctx, targets)
}

// observeEvery runs observe every interval until ctx is done.
func observeEvery(ctx context.Context, interval time.Duration, workers workerLister, s syncer, sc scraper) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		observe(ctx, workers, s, sc)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// newLogger returns a text or JSON logger whose records carry the host name.
func newLogger(format, hostName string, w io.Writer) *log.Logger {
	var h log.Handler = log.NewTextHandler(w, nil)
	if format == "json" {
		h = log.NewJSONHandler(w, nil)
	}
	return log.New(h).With("host", hostName)
}
