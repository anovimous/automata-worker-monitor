// workers-control-plane keeps the registry of the Automata workers, decides their health, runs the operator's actions
// through the worker machine controllers and serves the API and the UI.
package main

import (
	"context"
	"errors"
	"io"
	log "log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/buildinfo"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/api"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/controllerclient"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/fleet"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/sse"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	"github.com/anovimous/automata-worker-monitor/web"
)

const (
	defaultConfigPath = "/etc/automata-monitor/config.yml"
	purgeInterval     = time.Hour
	shutdownTimeout   = 10 * time.Second
)

func main() {
	os.Exit(run())
}

// run returns the exit code, so that the deferred cleanup runs first.
func run() int {
	log.SetDefault(newLogger(os.Getenv("LOG_FORMAT"), os.Stderr))
	path := os.Getenv("MONITOR_CONFIG")
	if path == "" {
		path = defaultConfigPath
	}
	cfg, err := config.Load(path, os.Getenv)
	if err != nil {
		log.Error("failed to load the configuration", "error", err)
		return 1
	}
	if !cfg.ListenIsLoopback() {
		// there is no login: whoever reaches this address can run every action
		log.Warn("the control plane listens on an address other than loopback and has no login; make sure only the operators can reach it",
			"listen", cfg.Listen)
	}

	st, err := store.Open(cfg.Database)
	if err != nil {
		log.Error("failed to open the database", "error", err)
		return 1
	}
	defer st.Close()
	now := func() time.Time { return time.Now().UTC() }
	if cfg.Seed() {
		if err := st.SeedDefault(context.Background(), now()); err != nil {
			log.Error("failed to seed the default template", "error", err)
			return 1
		}
	}

	clients := map[string]fleet.Client{}
	for _, h := range cfg.Hosts {
		c, err := controllerclient.New(h)
		if err != nil {
			log.Error("failed to set up a host", "host", h.Name, "error", err)
			return 1
		}
		clients[h.Name] = c
	}
	broker := sse.NewBroker()
	fl := fleet.New(cfg, st, clients, broker, now)
	if err := fl.Load(context.Background()); err != nil {
		log.Error("failed to load the fleet", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// the requests run in their own context, canceled first at the shutdown so that the event streams end
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	pollCtx, stopPolling := context.WithCancel(context.Background())
	polling := make(chan struct{})
	go func() {
		fl.Poll(pollCtx)
		close(polling)
	}()
	go fl.PurgeEvery(pollCtx, purgeInterval)

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: api.NewHandler(api.Deps{Fleet: fl, Store: st, Broker: broker, UI: web.Files(), PollIntervalSeconds: cfg.Polling.IntervalSeconds,
			HistoryRetentionDays: cfg.History.RetentionDays, Now: now}),
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// no write timeout: a stop waits for the worker, the event and log streams stay open
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Info("workers control plane listening", "addr", cfg.Listen, "tls", cfg.TLS.Cert != "", "hosts", len(cfg.Hosts),
			"version", buildinfo.Version)
		if cfg.TLS.Cert != "" {
			serveErr <- srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
		} else {
			serveErr <- srv.ListenAndServe()
		}
	}()

	code := 0
	select {
	case err := <-serveErr:
		log.Error("the API server stopped", "error", err)
		code = 1
	case <-ctx.Done():
		log.Info("shutting down")
	}

	cancelRequests()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("requests were still running at the shutdown", "error", err)
	}
	stopPolling()
	<-polling
	fl.Wait()
	// the minute in progress, merged with the rest of it after the restart
	fl.FlushHistory(context.Background())
	return code
}

// newLogger returns a JSON logger, or a text one with LOG_FORMAT=text.
func newLogger(format string, w io.Writer) *log.Logger {
	if format == "text" {
		return log.New(log.NewTextHandler(w, nil))
	}
	return log.New(log.NewJSONHandler(w, nil))
}
