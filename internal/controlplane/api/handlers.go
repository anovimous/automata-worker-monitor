package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/controllerclient"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/fleet"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	"github.com/anovimous/automata-worker-monitor/internal/redact"
)

// a list gives at most this many items
const maxLimit = 500

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Fleet.Overview())
}

func (s *server) configUI(w http.ResponseWriter, r *http.Request) {
	size, _ := s.Store.SizeBytes(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"pollIntervalSeconds":  s.PollIntervalSeconds,
		"historyRetentionDays": s.HistoryRetentionDays,
		"databaseBytes":        size,
	})
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	s.Broker.Serve(w, r, func() any {
		return map[string]any{"workers": s.Fleet.Workers(), "hosts": s.Fleet.Hosts()}
	})
}

func (s *server) hosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Fleet.Hosts())
}

func (s *server) host(w http.ResponseWriter, r *http.Request) {
	h, ok := s.Fleet.Host(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no host "+r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *server) cordon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	s.setCordon(w, r, true, req.Reason)
}

func (s *server) uncordon(w http.ResponseWriter, r *http.Request) {
	s.setCordon(w, r, false, "")
}

func (s *server) setCordon(w http.ResponseWriter, r *http.Request, cordoned bool, reason string) {
	name := r.PathValue("name")
	if err := s.Fleet.Cordon(r.Context(), name, cordoned, reason); err != nil {
		writeErr(w, err)
		return
	}
	h, _ := s.Fleet.Host(name)
	writeJSON(w, http.StatusOK, h)
}

// client is the worker machine controller of the host in the path.
func (s *server) client(w http.ResponseWriter, hostName string) (fleet.Client, bool) {
	c, ok := s.Fleet.Client(hostName)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no host "+hostName)
	}
	return c, ok
}

// controllerFailure answers an error of a worker machine controller: its refusals keep their status (except a 401,
// which is the control plane's configuration, not the caller's fault), the rest is a 502.
func controllerFailure(w http.ResponseWriter, err error) {
	var ae *controllerclient.APIError
	if errors.As(err, &ae) && ae.Status < 500 && ae.Status != http.StatusUnauthorized {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeError(w, http.StatusBadGateway, "WORKER_MACHINE_CONTROLLER", err.Error())
}

func shortCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 15*time.Second)
}

func (s *server) envFiles(w http.ResponseWriter, r *http.Request) {
	c, ok := s.client(w, r.PathValue("name"))
	if !ok {
		return
	}
	ctx, cancel := shortCtx(r)
	defer cancel()
	names, err := c.EnvFiles(ctx)
	if err != nil {
		controllerFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *server) images(w http.ResponseWriter, r *http.Request) {
	c, ok := s.client(w, r.PathValue("name"))
	if !ok {
		return
	}
	ctx, cancel := shortCtx(r)
	defer cancel()
	imgs, err := c.Images(ctx, r.URL.Query().Get("reference"))
	if err != nil {
		controllerFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imgs)
}

func seriesOf(r *http.Request) []string {
	var out []string
	for _, s := range strings.Split(r.URL.Query().Get("series"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (s *server) hostTimeseries(w http.ResponseWriter, r *http.Request) {
	series := seriesOf(r)
	if len(series) == 0 {
		series = fleet.HostSeriesNames
	}
	ts, err := s.Fleet.HostTimeseries(r.PathValue("name"), series, or(r.URL.Query().Get("range"), "15m"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func templateID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID", "the template id must be a number")
		return 0, false
	}
	return id, true
}

func (s *server) templates(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Templates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) template(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	t, err := s.Store.Template(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// templateInput is what a client sends for a template; the id and the times are the store's.
type templateInput struct {
	Name                      string            `json:"name"`
	Description               string            `json:"description"`
	Image                     string            `json:"image"`
	PullPolicy                string            `json:"pullPolicy"`
	EnvFile                   string            `json:"envFile"`
	Env                       map[string]string `json:"env"`
	Network                   string            `json:"network"`
	ExtraHosts                []string          `json:"extraHosts"`
	CPUs                      float64           `json:"cpus"`
	MemoryBytes               int64             `json:"memoryBytes"`
	PidsLimit                 int64             `json:"pidsLimit"`
	WordlistCacheMaxBytes     int64             `json:"wordlistCacheMaxBytes"`
	TmpfsSocketsBytes         int64             `json:"tmpfsSocketsBytes"`
	RestartMaxRetries         int               `json:"restartMaxRetries"`
	StopTimeoutSeconds        int               `json:"stopTimeoutSeconds"`
	DrainTimeoutSeconds       int               `json:"drainTimeoutSeconds"`
	EscalateAfterDrainTimeout bool              `json:"escalateAfterDrainTimeout"`
	RemoveAfterDrain          bool              `json:"removeAfterDrain"`
	ExtraLabels               map[string]string `json:"extraLabels"`
}

func (in templateInput) template() store.Template {
	t := store.Template{Name: in.Name, Description: in.Description, Image: in.Image, PullPolicy: in.PullPolicy,
		EnvFile: in.EnvFile, Env: in.Env, Network: in.Network, ExtraHosts: in.ExtraHosts, CPUs: in.CPUs,
		MemoryBytes: in.MemoryBytes, PidsLimit: in.PidsLimit, WordlistCacheMaxBytes: in.WordlistCacheMaxBytes,
		TmpfsSocketsBytes: in.TmpfsSocketsBytes, RestartMaxRetries: in.RestartMaxRetries, StopTimeoutSeconds: in.StopTimeoutSeconds,
		DrainTimeoutSeconds: in.DrainTimeoutSeconds, EscalateAfterDrainTimeout: in.EscalateAfterDrainTimeout,
		RemoveAfterDrain: in.RemoveAfterDrain, ExtraLabels: in.ExtraLabels}
	// the defaults of the table for the values left out
	if t.TmpfsSocketsBytes == 0 {
		t.TmpfsSocketsBytes = 16 << 20
	}
	if t.StopTimeoutSeconds == 0 {
		t.StopTimeoutSeconds = 60
	}
	if t.PidsLimit == 0 {
		t.PidsLimit = 512
	}
	return t
}

func (s *server) readTemplate(w http.ResponseWriter, r *http.Request) (store.Template, bool) {
	in := templateInput{RestartMaxRetries: 5}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return store.Template{}, false
	}
	t := in.template()
	if err := fleet.ValidateTemplate(t); err != nil {
		writeErr(w, err)
		return store.Template{}, false
	}
	return t, true
}

func (s *server) createTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.readTemplate(w, r)
	if !ok {
		return
	}
	created, err := s.Store.CreateTemplate(r.Context(), t, s.Now())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	t, ok := s.readTemplate(w, r)
	if !ok {
		return
	}
	t.ID = id
	updated, err := s.Store.UpdateTemplate(r.Context(), t, s.Now())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// deleteTemplate is allowed while workers use the template: each keeps its own copy.
func (s *server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteTemplate(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// limitOf reads ?limit (1–500, def when left out).
func limitOf(r *http.Request, def int) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxLimit {
		return 0, &fleet.Error{Status: http.StatusBadRequest, Code: "INVALID", Message: fmt.Sprintf("limit must be from 1 to %d", maxLimit)}
	}
	return n, nil
}

// workers lists the workers, filtered, a page at a time: ?cursor is the last id of the previous page.
func (s *server) workers(w http.ResponseWriter, r *http.Request) {
	limit, err := limitOf(r, maxLimit)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	f := fleet.Filter{Host: q.Get("host"), Health: q.Get("health"), Lifecycle: q.Get("lifecycle"),
		Phase: q.Get("phase"), Routine: q.Get("routine"), Q: q.Get("q")}
	cursor := q.Get("cursor")
	out := []fleet.WorkerView{}
	next := ""
	for _, v := range s.Fleet.Workers() {
		if v.ID <= cursor || !f.Match(v) {
			continue
		}
		if len(out) == limit {
			next = out[len(out)-1].ID
			break
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "nextCursor": next})
}

func (s *server) createWorkers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host       string `json:"host"`
		TemplateID int64  `json:"templateId"`
		Count      int    `json:"count"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Count == 0 {
		req.Count = 1
	}
	res, err := s.Fleet.Create(r.Context(), req.Host, req.TemplateID, req.Count)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// worker is the view of a worker, its template as used at the creation and what docker inspect says of it.
func (s *server) worker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, ok := s.Fleet.Worker(id)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no worker "+id)
		return
	}
	snapshot, _ := s.Fleet.TemplateSnapshot(id)
	resp := map[string]any{"worker": v, "templateSnapshot": json.RawMessage(snapshot), "container": nil, "containerError": ""}
	if c, ok := s.Fleet.Client(v.Host); ok {
		ctx, cancel := shortCtx(r)
		defer cancel()
		if d, err := c.Worker(ctx, id); err == nil {
			resp["container"] = d
		} else {
			resp["containerError"] = redact.Line(err.Error())
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

var actions = []string{"drain", "undrain", "stop", "kill", "restart", "start", "remove"}

func (s *server) action(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if !slices.Contains(actions, action) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no action "+action)
		return
	}
	var req struct {
		TimeoutSeconds int  `json:"timeoutSeconds"`
		RemoveVolumes  bool `json:"removeVolumes"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.TimeoutSeconds < 0 || req.TimeoutSeconds > 600 {
		writeError(w, http.StatusBadRequest, "INVALID", "timeoutSeconds must be from 1 to 600")
		return
	}
	res, err := s.Fleet.Act(r.Context(), r.PathValue("id"), action,
		fleet.ActionOptions{TimeoutSeconds: req.TimeoutSeconds, RemoveVolumes: req.RemoveVolumes})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) workerEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := limitOf(r, 100)
	if err != nil {
		writeErr(w, err)
		return
	}
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID", "before must be an event id")
			return
		}
	}
	evs, err := s.Store.Events(r.Context(), r.PathValue("id"), before, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

func (s *server) workerTimeseries(w http.ResponseWriter, r *http.Request) {
	series := seriesOf(r)
	if len(series) == 0 {
		series = fleet.WorkerSeriesNames
	}
	ts, err := s.Fleet.WorkerTimeseries(r.PathValue("id"), series, or(r.URL.Query().Get("range"), "15m"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

// logs passes the log stream of the worker machine controller on, redacted once more on the way.
func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	hostName, ok := s.Fleet.HostOf(id)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no worker "+id)
		return
	}
	c, _ := s.Fleet.Client(hostName)
	q := r.URL.Query()
	tail, _ := strconv.Atoi(q.Get("tail"))
	opts := controllerclient.LogOptions{Tail: tail, Since: q.Get("since"), Follow: q.Get("follow") == "true"}

	ctx := r.Context()
	if !opts.Follow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	body, _, err := c.Logs(ctx, id, opts)
	if err != nil {
		controllerFailure(w, err)
		return
	}
	defer body.Close()

	if !opts.Follow {
		var lines []controllerapi.LogLine
		if err := json.NewDecoder(io.LimitReader(body, 32<<20)).Decode(&lines); err != nil {
			writeError(w, http.StatusBadGateway, "WORKER_MACHINE_CONTROLLER", "bad log answer: "+err.Error())
			return
		}
		for i := range lines {
			lines[i].Line = redact.Line(lines[i].Line)
		}
		if lines == nil {
			lines = []controllerapi.LogLine{}
		}
		writeJSON(w, http.StatusOK, lines)
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var l controllerapi.LogLine
			if json.Unmarshal([]byte(data), &l) == nil {
				l.Line = redact.Line(l.Line)
				b, _ := json.Marshal(l)
				line = "data: " + string(b)
			} else {
				line = "data: " + redact.Line(data)
			}
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return
		}
		if line == "" && flusher != nil {
			flusher.Flush()
		}
	}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
