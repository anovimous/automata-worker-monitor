package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/controllerclient"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
	"github.com/anovimous/automata-worker-monitor/internal/redact"
)

// Error is a refused or failed request, with the HTTP status and the code the API answers.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func errorf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// controllerError turns an error of a worker machine controller into one of the API: its own refusals keep their
// status, the rest is a 502.
func controllerError(err error) *Error {
	var ae *controllerclient.APIError
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusUnauthorized {
		return errorf(ae.Status, ae.Code, "%s", redact.Line(ae.Message))
	}
	return errorf(http.StatusBadGateway, "WORKER_MACHINE_CONTROLLER", "%s", redact.Line(err.Error()))
}

// ActionOptions are the options of an action.
type ActionOptions struct {
	TimeoutSeconds int  // stop and restart; 0 is the template's stop timeout
	RemoveVolumes  bool // remove
	reason         string
}

// ActionResult is the worker after an action, with warnings for the operator (e.g. volumes left behind).
type ActionResult struct {
	Worker   WorkerView `json:"worker"`
	Warnings []string   `json:"warnings"`
}

// Act runs an action on a worker (plan/07 §5). The lifecycle moves first, so that a poll during a long call (a stop
// waits for the worker to report its job) sees what was asked for; a failed call puts it back.
func (f *Fleet) Act(ctx context.Context, id, action string, opts ActionOptions) (ActionResult, error) {
	unlock := f.lock(id)
	defer unlock()

	f.mu.Lock()
	w, ok := f.workers[id]
	if !ok {
		f.mu.Unlock()
		return ActionResult{}, errorf(http.StatusNotFound, "NOT_FOUND", "no worker %s", id)
	}
	h := f.hosts[w.row.Host]
	if err := lifecycle.Check(w.row.Lifecycle, action, h.cordoned); err != nil {
		f.mu.Unlock()
		if errors.Is(err, lifecycle.ErrHostCordoned) {
			return ActionResult{}, errorf(http.StatusConflict, "HOST_CORDONED", "%v", err)
		}
		return ActionResult{}, errorf(http.StatusConflict, "INVALID_TRANSITION", "%v", err)
	}
	now := f.now()
	prev := w.row
	f.addEvent(ctx, store.Event{WorkerID: id, At: now, Kind: store.EventAction, To: action})
	if next := lifecycle.AfterAction(action); next != w.row.Lifecycle {
		f.setLifecycle(ctx, w, next, opts.reason, "")
	}
	if action == lifecycle.ActionDrain && w.tmpl != nil && w.tmpl.DrainTimeoutSeconds > 0 {
		deadline := now.Add(time.Duration(w.tmpl.DrainTimeoutSeconds) * time.Second)
		w.row.DrainDeadline = &deadline
		f.persist(ctx, w)
	}
	timeout := f.stopTimeout(w, opts)
	client := f.clients[w.row.Host]
	f.publishWorker(w)
	f.mu.Unlock()

	obs, removed, err := f.call(ctx, client, id, action, timeout, opts.RemoveVolumes)

	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil && !(action == lifecycle.ActionRemove && controllerclient.IsNotFound(err)) {
		// the action did not happen: back to where it was
		f.addEvent(ctx, store.Event{WorkerID: id, At: f.now(), Kind: store.EventAction, To: action,
			Message: "failed: " + redact.Line(err.Error())})
		if w.row.Lifecycle != prev.Lifecycle || w.row.LifecycleReason != prev.LifecycleReason {
			f.setLifecycle(ctx, w, prev.Lifecycle, prev.LifecycleReason, "the action failed")
		}
		w.row.DrainDeadline = prev.DrainDeadline
		f.persist(ctx, w)
		f.publishWorker(w)
		return ActionResult{}, controllerError(err)
	}

	res := ActionResult{Warnings: []string{}}
	if obs != nil {
		w.obs = obs
	}
	if action == lifecycle.ActionRemove {
		if len(removed.VolumesNotRemoved) > 0 {
			var names []string
			for _, v := range removed.VolumesNotRemoved {
				names = append(names, v.Name)
				res.Warnings = append(res.Warnings, fmt.Sprintf("the volume %s was not removed: %s; remove it with docker volume rm", v.Name, v.Error))
			}
			w.row.LifecycleReason = "VOLUMES_NOT_REMOVED: " + strings.Join(names, ", ")
			f.persist(ctx, w)
		}
		// the container is gone: removed now rather than at the next poll
		view := f.viewOf(w)
		f.setLifecycle(ctx, w, lifecycle.Removed, w.row.LifecycleReason, "")
		view.Lifecycle = lifecycle.Removed
		res.Worker = view
		return res, nil
	}
	f.publishWorker(w)
	res.Worker = f.viewOf(w)
	return res, nil
}

// stopTimeout is the stop timeout of an action: the request's, else the template's, else 60 s.
func (f *Fleet) stopTimeout(w *worker, opts ActionOptions) time.Duration {
	secs := 60
	if w.tmpl != nil && w.tmpl.StopTimeoutSeconds > 0 {
		secs = w.tmpl.StopTimeoutSeconds
	}
	if opts.TimeoutSeconds > 0 {
		secs = opts.TimeoutSeconds
	}
	return time.Duration(secs) * time.Second
}

// call sends an action to the worker machine controller, with a deadline that leaves a stop its timeout.
func (f *Fleet) call(ctx context.Context, c Client, id, action string, stopTimeout time.Duration, removeVolumes bool) (*controllerapi.WorkerObservation, controllerapi.RemoveResponse, error) {
	limit := 30 * time.Second
	if action == lifecycle.ActionStop || action == lifecycle.ActionRestart {
		limit = stopTimeout + 30*time.Second
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	var o controllerapi.WorkerObservation
	var err error
	switch action {
	case lifecycle.ActionDrain:
		o, err = c.Signal(ctx, id, controllerapi.SignalDrain)
	case lifecycle.ActionUndrain:
		o, err = c.Signal(ctx, id, controllerapi.SignalUndrain)
	case lifecycle.ActionStop:
		o, err = c.Stop(ctx, id, stopTimeout)
	case lifecycle.ActionKill:
		o, err = c.Kill(ctx, id)
	case lifecycle.ActionRestart:
		o, err = c.Restart(ctx, id, stopTimeout)
	case lifecycle.ActionStart:
		o, err = c.Start(ctx, id)
	case lifecycle.ActionRemove:
		r, err := c.Remove(ctx, id, removeVolumes)
		return nil, r, err
	default:
		return nil, controllerapi.RemoveResponse{}, fmt.Errorf("unknown action %s", action)
	}
	if err != nil {
		return nil, controllerapi.RemoveResponse{}, err
	}
	return &o, controllerapi.RemoveResponse{}, nil
}

// MaxCreate is the most workers one request creates.
const MaxCreate = 20

// CreatedWorker is one worker of a create request.
type CreatedWorker struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type CreateResult struct {
	Workers  []CreatedWorker `json:"workers"`
	Warnings []string        `json:"warnings"`
}

// Create provisions count workers of a template on a host, one after the other (plan/07 §6).
func (f *Fleet) Create(ctx context.Context, hostName string, templateID int64, count int) (CreateResult, error) {
	f.createMu.Lock()
	defer f.createMu.Unlock()
	if count < 1 || count > MaxCreate {
		return CreateResult{}, errorf(http.StatusBadRequest, "INVALID", "count must be from 1 to %d", MaxCreate)
	}
	tmpl, err := f.store.Template(ctx, templateID)
	if errors.Is(err, store.ErrNotFound) {
		return CreateResult{}, errorf(http.StatusNotFound, "NOT_FOUND", "no template %d", templateID)
	}
	if err != nil {
		return CreateResult{}, err
	}

	f.mu.Lock()
	h, ok := f.hosts[hostName]
	if !ok {
		f.mu.Unlock()
		return CreateResult{}, errorf(http.StatusNotFound, "NOT_FOUND", "no host %s", hostName)
	}
	if h.cordoned {
		f.mu.Unlock()
		return CreateResult{}, errorf(http.StatusConflict, "HOST_CORDONED", "the host %s is cordoned: %s", hostName, h.cordonedReason)
	}
	// a stopped worker keeps its slot, so that starting it never goes over the limit
	held := 0
	for _, w := range f.workers {
		if w.row.Host == hostName {
			held++
		}
	}
	if held+count > h.cfg.MaxWorkers {
		f.mu.Unlock()
		return CreateResult{}, errorf(http.StatusConflict, "MAX_WORKERS", "the host %s has %d of its %d workers, %d more would go over",
			hostName, held, h.cfg.MaxWorkers, count)
	}
	warnings := f.capacityWarnings(h, tmpl, count)
	client := f.clients[hostName]
	f.mu.Unlock()

	snapshot, err := json.Marshal(tmpl)
	if err != nil {
		return CreateResult{}, err
	}
	res := CreateResult{Workers: []CreatedWorker{}, Warnings: warnings}
	for range count {
		id, err := f.newID(ctx, hostName)
		if err != nil {
			return res, err
		}
		res.Workers = append(res.Workers, f.createOne(ctx, client, hostName, id, tmpl, snapshot))
	}
	return res, nil
}

func (f *Fleet) createOne(ctx context.Context, client Client, hostName, id string, tmpl store.Template, snapshot []byte) CreatedWorker {
	now := f.now()
	row := store.Worker{ID: id, Host: hostName, TemplateID: &tmpl.ID, TemplateSnapshot: snapshot, Managed: true,
		Lifecycle: lifecycle.Provisioning, LifecycleSince: now, Health: "STARTING", HealthReasons: []string{},
		HealthSince: now, CreatedAt: now}
	f.mu.Lock()
	if err := f.store.InsertWorker(ctx, row); err != nil {
		f.mu.Unlock()
		return CreatedWorker{ID: id, Error: err.Error()}
	}
	w := f.newWorker(row)
	f.workers[id] = w
	f.addEvent(ctx, store.Event{WorkerID: id, At: now, Kind: store.EventAction, To: "create",
		Message: fmt.Sprintf("template %s on %s", tmpl.Name, hostName)})
	f.publishWorker(w)
	f.mu.Unlock()

	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute) // a pull may take a while
	defer cancel()
	resp, err := client.Create(callCtx, controllerapi.CreateWorkerRequest{WorkerID: id, Template: Spec(tmpl)})

	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		var ae *controllerclient.APIError
		if errors.As(err, &ae) && strings.Contains(ae.Message, "(container ") {
			// the container exists but did not start: it is kept for its logs
			w.row.ContainerID = between(ae.Message, "(container ", ")")
		}
		msg := redact.Line(err.Error())
		f.addEvent(ctx, store.Event{WorkerID: id, At: f.now(), Kind: store.EventProvisionFailed, Message: msg})
		f.setLifecycle(ctx, w, lifecycle.ProvisionFailed, "CREATE_FAILED", msg)
		f.publishWorker(w)
		return CreatedWorker{ID: id, Error: msg}
	}
	w.row.ContainerID = resp.ContainerID
	f.persist(ctx, w)
	f.publishWorker(w)
	return CreatedWorker{ID: id, OK: true}
}

func between(s, start, end string) string {
	_, rest, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, end)
	return v
}

const idLetters = "abcdefghijklmnopqrstuvwxyz0123456789"

// newID is w-<host>-<6 random characters>, drawn again on a collision.
func (f *Fleet) newID(ctx context.Context, hostName string) (string, error) {
	for range 10 {
		b := make([]byte, 6)
		if _, err := f.random.Read(b); err != nil {
			return "", err
		}
		for i := range b {
			b[i] = idLetters[int(b[i])%len(idLetters)]
		}
		id := "w-" + hostName + "-" + string(b)
		f.mu.Lock()
		_, inView := f.workers[id]
		f.mu.Unlock()
		if _, err := f.store.Worker(ctx, id); !inView && errors.Is(err, store.ErrNotFound) {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not draw a free worker id")
}

// capacityWarnings compares the limits of the host's workers and the new ones with the host (a warning only).
func (f *Fleet) capacityWarnings(h *host, tmpl store.Template, count int) []string {
	warnings := []string{}
	if h.metrics == nil {
		return warnings
	}
	cpus, mem := f.reserved(h.cfg.Name)
	cpus += float64(count) * tmpl.CPUs
	mem += int64(count) * tmpl.MemoryBytes
	if cpus > float64(h.metrics.CPUs) || mem > h.metrics.MemoryTotalBytes {
		warnings = append(warnings, fmt.Sprintf("these %d workers would bring %s to %.1f / %d CPUs and %.1f / %.1f GB",
			count, h.cfg.Name, cpus, h.metrics.CPUs, float64(mem)/(1<<30), float64(h.metrics.MemoryTotalBytes)/(1<<30)))
	}
	return warnings
}

// reserved sums the limits of the workers of a host that are, or are meant to be, running.
func (f *Fleet) reserved(hostName string) (cpus float64, mem int64) {
	for _, w := range f.workers {
		if w.row.Host != hostName || w.tmpl == nil {
			continue
		}
		switch w.row.Lifecycle {
		case lifecycle.Provisioning, lifecycle.Active, lifecycle.Draining, lifecycle.Stopping:
			cpus += w.tmpl.CPUs
			mem += w.tmpl.MemoryBytes
		}
	}
	return cpus, mem
}

// Spec is the template in the form of the worker machine controller's create request.
func Spec(t store.Template) controllerapi.WorkerSpec {
	restart := controllerapi.RestartPolicy{Name: "no"}
	if t.RestartMaxRetries > 0 {
		restart = controllerapi.RestartPolicy{Name: "on-failure", MaxRetries: t.RestartMaxRetries}
	}
	env, labels, hosts := t.Env, t.ExtraLabels, t.ExtraHosts
	if env == nil {
		env = map[string]string{}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	if hosts == nil {
		hosts = []string{}
	}
	return controllerapi.WorkerSpec{
		Name: t.Name, Image: t.Image, PullPolicy: t.PullPolicy, EnvFile: t.EnvFile, Env: env, Network: t.Network,
		ExtraHosts: hosts, CPUs: t.CPUs, MemoryBytes: t.MemoryBytes, PidsLimit: t.PidsLimit,
		WordlistCacheMaxBytes: t.WordlistCacheMaxBytes, TmpfsSocketsBytes: t.TmpfsSocketsBytes, RestartPolicy: restart,
		StopTimeoutSeconds: t.StopTimeoutSeconds, ExtraLabels: labels,
	}
}

// ValidateTemplate checks a template as the worker machine controller will, plus: the image has a tag, not latest.
func ValidateTemplate(t store.Template) error {
	if err := provision.Validate(controllerapi.CreateWorkerRequest{WorkerID: "w-check-aaaaaa", Template: Spec(t)}); err != nil {
		return errorf(http.StatusBadRequest, "INVALID", "%s", strings.ReplaceAll(err.Message, "template.", ""))
	}
	if !hasTag(t.Image) {
		return errorf(http.StatusBadRequest, "INVALID", "image must name a tag other than latest (e.g. automata-worker:0.4.0)")
	}
	if t.DrainTimeoutSeconds < 0 || t.RestartMaxRetries < 0 {
		return errorf(http.StatusBadRequest, "INVALID", "drainTimeoutSeconds and restartMaxRetries must not be negative")
	}
	if len(t.Description) > 500 {
		return errorf(http.StatusBadRequest, "INVALID", "description must be at most 500 characters")
	}
	return nil
}

var tagPattern = regexp.MustCompile(`:([A-Za-z0-9_][A-Za-z0-9_.-]{0,127})$`)

// hasTag tells whether an image names a tag (or a digest), and not latest: a template must say which build it runs.
func hasTag(image string) bool {
	if strings.Contains(image, "@sha256:") {
		return true
	}
	name := image[strings.LastIndex(image, "/")+1:]
	m := tagPattern.FindStringSubmatch(name)
	return m != nil && m[1] != "latest"
}

// Cordon stops (or allows again) the creation of workers on a host.
func (f *Fleet) Cordon(ctx context.Context, hostName string, cordoned bool, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[hostName]
	if !ok {
		return errorf(http.StatusNotFound, "NOT_FOUND", "no host %s", hostName)
	}
	if len(reason) > 200 {
		return errorf(http.StatusBadRequest, "INVALID", "reason must be at most 200 characters")
	}
	if err := f.store.SetCordon(ctx, hostName, cordoned, reason); err != nil {
		return err
	}
	h.cordoned = cordoned
	h.cordonedReason = ""
	if cordoned {
		h.cordonedReason = reason
	}
	f.publishHost(h)
	for _, w := range f.workers {
		if w.row.Host == hostName {
			f.publishWorker(w)
		}
	}
	return nil
}

// Client returns the client of a host's worker machine controller.
func (f *Fleet) Client(hostName string) (Client, bool) {
	c, ok := f.clients[hostName]
	return c, ok
}

// HostOf returns the host of a worker.
func (f *Fleet) HostOf(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[id]
	if !ok {
		return "", false
	}
	return w.row.Host, true
}
