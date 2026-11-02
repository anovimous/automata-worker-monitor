// Package api is the REST and SSE API of the workers control plane for its UI (plan/07-workers-control-plane.md §7),
// and serves the UI itself. There is no login (O3 rejected): anyone who reaches the control plane can run every
// action. The one protection kept is the Origin check, against the other web pages open in the operator's browser.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	log "log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controlplane/fleet"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/sse"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	"github.com/anovimous/automata-worker-monitor/internal/redact"
)

// a request body is at most this big (a template is the biggest)
const maxBody = 64 << 10

//go:embed ui
var placeholderUI embed.FS

// Deps is what the handlers use.
type Deps struct {
	Fleet  *fleet.Fleet
	Store  *store.Store
	Broker *sse.Broker
	// UI is the web UI (web/dist); nil serves the placeholder page
	UI                   fs.FS
	PollIntervalSeconds  int
	HistoryRetentionDays int
	Now                  func() time.Time
}

type server struct {
	Deps
}

// NewHandler returns the API under /api/v1, GET /healthz and the UI for every other path.
func NewHandler(d Deps) http.Handler {
	if d.UI == nil {
		d.UI, _ = fs.Sub(placeholderUI, "ui")
	}
	s := &server{Deps: d}
	mux := http.NewServeMux()
	route := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }

	route("GET /api/v1/overview", s.overview)
	route("GET /api/v1/config/ui", s.configUI)
	route("GET /api/v1/events", s.events)
	route("GET /api/v1/availability", s.availability)
	route("GET /api/v1/fleet/timeseries", s.fleetTimeseries)

	route("GET /api/v1/hosts", s.hosts)
	route("GET /api/v1/hosts/{name}", s.host)
	route("POST /api/v1/hosts/{name}/cordon", s.cordon)
	route("POST /api/v1/hosts/{name}/uncordon", s.uncordon)
	route("GET /api/v1/hosts/{name}/env-files", s.envFiles)
	route("GET /api/v1/hosts/{name}/images", s.images)
	route("GET /api/v1/hosts/{name}/timeseries", s.hostTimeseries)

	route("GET /api/v1/templates", s.templates)
	route("GET /api/v1/templates/{id}", s.template)
	route("POST /api/v1/templates", s.createTemplate)
	route("PUT /api/v1/templates/{id}", s.updateTemplate)
	route("DELETE /api/v1/templates/{id}", s.deleteTemplate)

	route("GET /api/v1/workers", s.workers)
	route("POST /api/v1/workers", s.createWorkers)
	route("GET /api/v1/workers/{id}", s.worker)
	route("POST /api/v1/workers/{id}/actions/{action}", s.action)
	route("GET /api/v1/workers/{id}/events", s.workerEvents)
	route("GET /api/v1/workers/{id}/timeseries", s.workerTimeseries)
	route("GET /api/v1/workers/{id}/availability", s.workerAvailability)
	route("GET /api/v1/workers/{id}/logs", s.logs)

	route("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such route")
	})
	route("GET /healthz", s.healthz)
	route("/", s.ui)
	return logRequests(checkOrigin(mux))
}

// checkOrigin refuses a request sent by another site: a request whose Origin header is not the control plane's own.
// A request without Origin (curl, a same-origin GET) is allowed.
func checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				writeError(w, http.StatusForbidden, "FORBIDDEN_ORIGIN", "requests from another site are refused")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder keeps the status of a response for the log, and passes Flush on for the event streams.
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

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn("failed to write a response", "error", err)
	}
}

func writeError(w http.ResponseWriter, code int, errCode, message string) {
	var b errorBody
	b.Error.Code, b.Error.Message = errCode, redact.Line(message)
	writeJSON(w, code, b)
}

// writeErr answers an error of the fleet or of the store with its status.
func writeErr(w http.ResponseWriter, err error) {
	var fe *fleet.Error
	switch {
	case errors.As(err, &fe):
		writeError(w, fe.Status, fe.Code, fe.Message)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "CONFLICT", err.Error())
	default:
		log.Error("a request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the request failed, see the log of the control plane")
	}
}

// decode reads a JSON body strictly: a field the API does not know is refused, so that a typo does not pass silently.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return nil
	case errors.As(err, &tooBig):
		return &fleet.Error{Status: http.StatusRequestEntityTooLarge, Code: "INVALID", Message: fmt.Sprintf("the body is bigger than %d bytes", maxBody)}
	default:
		return &fleet.Error{Status: http.StatusBadRequest, Code: "INVALID", Message: "the body is not valid: " + err.Error()}
	}
}

// healthz tells whether the control plane works: the database answers and a host was polled within 30 s.
func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		http.Error(w, "the database does not answer", http.StatusServiceUnavailable)
		return
	}
	if last := s.Fleet.LastPollAt(); last == nil || s.Now().Sub(*last) > 30*time.Second {
		http.Error(w, "no host was polled in the last 30 s", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "ok")
}

// ui serves the files of the UI, and its index.html for any other path, so that the client-side router works.
func (s *server) ui(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name != "" {
		if f, err := s.UI.Open(name); err == nil {
			stat, err := f.Stat()
			f.Close()
			if err == nil && !stat.IsDir() {
				http.ServeFileFS(w, r, s.UI, name)
				return
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFileFS(w, r, s.UI, "index.html")
}
