// Package api is the HTTP API of the worker machine controller (plan/06-worker-machine-controller.md §3). Its only
// caller is the workers control plane, authenticated by a bearer token. There is no generic Docker route: only the
// worker actions below, and signals limited to drain and undrain.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	log "log/slog"
	"net/http"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
	"github.com/anovimous/automata-worker-monitor/internal/redact"
)

// a request body is at most this big (the biggest one is a create request)
const maxBody = 64 << 10

// Workers is the list of the worker containers (discovery.Discovery).
type Workers interface {
	Workers() []discovery.Container
	Find(workerID string) (discovery.Container, bool)
	Update(ctx context.Context, containerID string)
}

// Stats gives the latest resources of a container (stats.Manager).
type Stats interface {
	Get(containerID string) (controllerapi.Resources, bool)
}

// Scrapes gives the last scrape of a container (scrape.Scraper).
type Scrapes interface {
	Get(containerID string) (controllerapi.Telemetry, bool)
}

// Volumes gives the size of the worker volumes (volumes.Sampler).
type Volumes interface {
	Get(workerID string) *controllerapi.Volumes
	Total() *int64
}

// Host gives the latest sample of the host (hostmetrics.Sampler).
type Host interface {
	Latest() controllerapi.HostMetrics
}

// Provisioner creates workers (provision.Provisioner).
type Provisioner interface {
	Create(ctx context.Context, req controllerapi.CreateWorkerRequest) (controllerapi.CreateWorkerResponse, error)
}

// Deps is what the handlers use.
type Deps struct {
	HostName         string
	Version          string
	Token            string
	EnvDir           string
	DockerVersion    string
	DockerAPIVersion string
	Engine           docker.Engine
	Workers          Workers
	Stats            Stats
	Scrapes          Scrapes
	Volumes          Volumes
	Host             Host
	Provisioner      Provisioner
	Now              func() time.Time
}

type server struct {
	Deps
}

// NewHandler returns the routes of the API, behind the token check and the request log.
func NewHandler(d Deps) http.Handler {
	s := &server{Deps: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/snapshot", s.snapshot)
	mux.HandleFunc("POST /v1/workers", s.createWorker)
	mux.HandleFunc("GET /v1/workers/{id}", s.getWorker)
	mux.HandleFunc("POST /v1/workers/{id}/start", s.start)
	mux.HandleFunc("POST /v1/workers/{id}/signal", s.signal)
	mux.HandleFunc("POST /v1/workers/{id}/stop", s.stop)
	mux.HandleFunc("POST /v1/workers/{id}/kill", s.kill)
	mux.HandleFunc("POST /v1/workers/{id}/restart", s.restart)
	mux.HandleFunc("DELETE /v1/workers/{id}", s.remove)
	mux.HandleFunc("GET /v1/workers/{id}/logs", s.logs)
	mux.HandleFunc("GET /v1/env-files", s.envFiles)
	mux.HandleFunc("GET /v1/images", s.images)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, controllerapi.CodeNotFound, "no such route")
	})
	return logRequests(s.authenticate(mux))
}

// authenticate refuses a request without the token; the comparison takes the same time whatever the token sent.
func (s *server) authenticate(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			writeError(w, http.StatusUnauthorized, controllerapi.CodeUnauthorized, "a valid bearer token is required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder keeps the status of a response for the log, and passes Flush on for the log stream.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests logs each request without its body or query, which are the caller's business.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		level := log.LevelDebug
		if rec.status >= 400 || r.Method != http.MethodGet {
			level = log.LevelInfo
		}
		log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"durationMs", time.Since(start).Milliseconds())
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn("failed to write a response", "error", err)
	}
}

func writeError(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, controllerapi.ErrorResponse{Error: controllerapi.ErrorBody{Code: errCode, Message: redact.Line(message)}})
}

// writeDockerError answers an error of the Docker Engine: 404 and 409 for what Docker refused, 502 otherwise.
func writeDockerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, docker.ErrNotFound):
		writeError(w, http.StatusNotFound, controllerapi.CodeNotFound, err.Error())
	case errors.Is(err, docker.ErrConflict):
		writeError(w, http.StatusConflict, controllerapi.CodeConflict, err.Error())
	default:
		writeError(w, http.StatusBadGateway, controllerapi.CodeDocker, err.Error())
	}
}

// writeProvisionError answers a failed create with the code the provisioner chose.
func writeProvisionError(w http.ResponseWriter, err error) {
	var pe *provision.Error
	if !errors.As(err, &pe) {
		writeError(w, http.StatusBadGateway, controllerapi.CodeDocker, err.Error())
		return
	}
	status := map[string]int{
		controllerapi.CodeInvalid:         http.StatusBadRequest,
		controllerapi.CodeImageNotAllowed: http.StatusBadRequest,
		controllerapi.CodeConflict:        http.StatusConflict,
	}[pe.Code]
	if status == 0 {
		status = http.StatusBadGateway
	}
	msg := pe.Message
	if pe.ContainerID != "" {
		msg = fmt.Sprintf("%s (container %s)", msg, pe.ContainerID)
	}
	writeError(w, status, pe.Code, msg)
}

// decode reads a JSON body; an empty body leaves v as it is.
func decode(r *http.Request, v any) error {
	err := json.NewDecoder(r.Body).Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return nil
	case errors.As(err, &tooBig):
		return fmt.Errorf("the body is bigger than %d bytes", maxBody)
	default:
		return fmt.Errorf("the body is not valid JSON: %w", err)
	}
}
