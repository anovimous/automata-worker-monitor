package main

import (
	"context"
	"encoding/json"
	"fmt"
	log "log/slog"
	"net"
	"net/http"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// statusSource is what the handlers read; *fake implements it.
type statusSource interface {
	snapshot() workerapi.Status
	health() (ok bool, progressAge time.Duration, phase workerapi.Phase)
}

// server is the telemetry server of the telemetry contract (GET /healthz and GET /status).
type server struct {
	addr string
	srv  *http.Server
}

func newServer(addr string, src statusSource, statusDelay time.Duration) *server {
	return &server{addr: addr, srv: &http.Server{
		Handler:           handler(src, statusDelay),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}}
}

// Start binds synchronously, so that a bad address stops the start, and serves in a goroutine.
func (s *server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("telemetry server stopped", "error", err)
		}
	}()
	return nil
}

func (s *server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// handler serves GET (and HEAD) /healthz and /status; the mux answers 405 to other methods and 404 elsewhere.
// statusDelay is the slow-status scenario.
func handler(src statusSource, statusDelay time.Duration) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ok, age, phase := src.health()
		if ok {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "ok")
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"reason":             "STUCK",
			"phase":              phase,
			"progressAgeSeconds": int(age.Seconds()),
		})
	})

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		if statusDelay > 0 {
			select {
			case <-time.After(statusDelay):
			case <-r.Context().Done():
				return
			}
		}
		writeJSON(w, http.StatusOK, src.snapshot())
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn("failed to write a telemetry response", "error", err)
	}
}

// healthcheck is the Docker HEALTHCHECK command, like the worker's `orchestrator healthcheck`: GET /healthz on
// TELEMETRY_ADDR, 0 for a 200, 1 otherwise; 0 when the telemetry is off.
func healthcheck(telemetryAddr string, client *http.Client) int {
	addr, err := parseTelemetryAddr(telemetryAddr)
	if err != nil {
		log.Error("healthcheck", "error", err)
		return 1
	}
	if addr == "" {
		return 0
	}

	host, port, _ := net.SplitHostPort(addr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		log.Error("healthcheck", "error", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
