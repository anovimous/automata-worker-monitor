package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/logs"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// the stop and restart timeout when the request names none: the monitor's default
const defaultStopTimeout = 60

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, controllerapi.HealthResponse{
		HostName:                       s.HostName,
		WorkerMachineControllerVersion: s.Version,
		Docker:                         controllerapi.DockerEngine{Version: s.DockerVersion, APIVersion: s.DockerAPIVersion},
		Now:                            s.Now().UTC(),
	})
}

func (s *server) snapshot(w http.ResponseWriter, r *http.Request) {
	host := s.Host.Latest()
	host.VolumesBytes = s.Volumes.Total()
	workers := []controllerapi.WorkerObservation{}
	for _, c := range s.Workers.Workers() {
		workers = append(workers, s.observation(c))
	}
	writeJSON(w, http.StatusOK, controllerapi.Snapshot{
		HostName:                       s.HostName,
		WorkerMachineControllerVersion: s.Version,
		Now:                            s.Now().UTC(),
		Host:                           host,
		Workers:                        workers,
	})
}

// observation is what the controller knows of one worker container.
func (s *server) observation(c discovery.Container) controllerapi.WorkerObservation {
	o := controllerapi.WorkerObservation{
		WorkerID: c.WorkerID,
		Managed:  c.Labels[workerapi.LabelManagedBy] == workerapi.ManagedByMonitor,
		Template: c.Labels[workerapi.LabelTemplate],
		Container: controllerapi.Container{
			ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, Status: c.Status, Health: c.Health,
			ExitCode: c.ExitCode, StartedAt: timeOrNil(c.StartedAt), FinishedAt: timeOrNil(c.FinishedAt),
			RestartCount: c.RestartCount, OOMKilled: c.OOMKilled, OOMEvents: c.OOMEvents,
			NetworkMode: c.NetworkMode, IP: c.IP, Labels: c.Labels, Limits: limitsOf(c.ContainerInfo),
		},
		Volumes: s.Volumes.Get(c.WorkerID),
		Flags:   []string{},
	}
	if c.Labels == nil {
		o.Container.Labels = map[string]string{}
	}
	if c.Running() {
		if r, ok := s.Stats.Get(c.ID); ok {
			o.Resources = &r
		}
	}

	o.Telemetry = controllerapi.Telemetry{Configured: c.TelemetryConfigured, URL: c.TelemetryURL}
	if t, ok := s.Scrapes.Get(c.ID); ok && c.Running() {
		o.Telemetry = t
	}

	if c.Duplicate {
		o.Flags = append(o.Flags, controllerapi.FlagDuplicateID)
	}
	if !c.TelemetryConfigured {
		o.Flags = append(o.Flags, controllerapi.FlagNoTelemetry)
	}
	return o
}

func limitsOf(c docker.ContainerInfo) controllerapi.Limits {
	var l controllerapi.Limits
	if c.NanoCPUs > 0 {
		cpus := float64(c.NanoCPUs) / 1e9
		l.CPUs = &cpus
	}
	if c.MemoryBytes > 0 {
		m := c.MemoryBytes
		l.MemoryBytes = &m
	}
	if c.PidsLimit > 0 {
		p := c.PidsLimit
		l.Pids = &p
	}
	return l
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// find answers 404 for an unknown worker.
func (s *server) find(w http.ResponseWriter, r *http.Request) (discovery.Container, bool) {
	id := r.PathValue("id")
	c, ok := s.Workers.Find(id)
	if !ok {
		writeError(w, http.StatusNotFound, controllerapi.CodeNotFound, fmt.Sprintf("no worker %s on this host", id))
	}
	return c, ok
}

func (s *server) getWorker(w http.ResponseWriter, r *http.Request) {
	c, ok := s.find(w, r)
	if !ok {
		return
	}
	info, err := s.Engine.Inspect(r.Context(), c.ID)
	if err != nil {
		writeDockerError(w, err)
		return
	}
	mounts := []controllerapi.Mount{}
	for _, m := range info.Mounts {
		mounts = append(mounts, controllerapi.Mount{Type: m.Type, Source: m.Source, Destination: m.Destination})
	}
	keys := append([]string{}, info.EnvKeys...)
	sort.Strings(keys)
	writeJSON(w, http.StatusOK, controllerapi.WorkerDetail{
		WorkerObservation: s.observation(c),
		Details: controllerapi.ContainerDetails{
			ImageID: info.ImageID, Created: info.Created.UTC(), EnvKeys: keys, Mounts: mounts,
			RestartPolicy: controllerapi.RestartPolicy{Name: info.Restart.Name, MaxRetries: info.Restart.MaxRetries},
			StopTimeout:   info.StopTimeout,
		},
	})
}

func (s *server) createWorker(w http.ResponseWriter, r *http.Request) {
	var req controllerapi.CreateWorkerRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, controllerapi.CodeInvalid, err.Error())
		return
	}
	resp, err := s.Provisioner.Create(r.Context(), req)
	var pe *provision.Error
	if errors.As(err, &pe) && pe.ContainerID != "" {
		// a container that exists (conflict, or did not start) is shown at once
		s.Workers.Update(r.Context(), pe.ContainerID)
	}
	if err != nil {
		writeProvisionError(w, err)
		return
	}
	s.Workers.Update(r.Context(), resp.ContainerID)
	writeJSON(w, http.StatusCreated, resp)
}

// act runs an action on the container of a worker and answers its observation after it.
func (s *server) act(w http.ResponseWriter, r *http.Request, action func(ctx context.Context, c discovery.Container) error) {
	c, ok := s.find(w, r)
	if !ok {
		return
	}
	if err := action(r.Context(), c); err != nil {
		writeDockerError(w, err)
		return
	}
	s.Workers.Update(r.Context(), c.ID)
	if c, ok = s.Workers.Find(c.WorkerID); !ok {
		writeError(w, http.StatusNotFound, controllerapi.CodeNotFound, "the worker is gone")
		return
	}
	writeJSON(w, http.StatusOK, s.observation(c))
}

func (s *server) start(w http.ResponseWriter, r *http.Request) {
	s.act(w, r, func(ctx context.Context, c discovery.Container) error { return s.Engine.Start(ctx, c.ID) })
}

func (s *server) signal(w http.ResponseWriter, r *http.Request) {
	var req controllerapi.SignalRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, controllerapi.CodeInvalid, err.Error())
		return
	}
	// only drain and undrain: any other signal is a stop or a kill, which have their own routes
	if req.Signal != controllerapi.SignalDrain && req.Signal != controllerapi.SignalUndrain {
		writeError(w, http.StatusBadRequest, controllerapi.CodeInvalid, "signal must be SIGUSR1 or SIGUSR2")
		return
	}
	s.act(w, r, func(ctx context.Context, c discovery.Container) error { return s.Engine.Kill(ctx, c.ID, req.Signal) })
}

// stopTimeout reads {"timeoutSeconds": n}: 1 to 600, the default when left out.
func stopTimeout(r *http.Request) (time.Duration, error) {
	var req controllerapi.StopRequest
	if err := decode(r, &req); err != nil {
		return 0, err
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = defaultStopTimeout
	}
	if req.TimeoutSeconds < 1 || req.TimeoutSeconds > 600 {
		return 0, fmt.Errorf("timeoutSeconds must be from 1 to 600")
	}
	return time.Duration(req.TimeoutSeconds) * time.Second, nil
}

func (s *server) stop(w http.ResponseWriter, r *http.Request) {
	timeout, err := stopTimeout(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, controllerapi.CodeInvalid, err.Error())
		return
	}
	s.act(w, r, func(ctx context.Context, c discovery.Container) error { return s.Engine.Stop(ctx, c.ID, timeout) })
}

func (s *server) kill(w http.ResponseWriter, r *http.Request) {
	s.act(w, r, func(ctx context.Context, c discovery.Container) error { return s.Engine.Kill(ctx, c.ID, "SIGKILL") })
}

func (s *server) restart(w http.ResponseWriter, r *http.Request) {
	timeout, err := stopTimeout(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, controllerapi.CodeInvalid, err.Error())
		return
	}
	s.act(w, r, func(ctx context.Context, c discovery.Container) error { return s.Engine.Restart(ctx, c.ID, timeout) })
}

// remove deletes a stopped worker container, then, when asked, its two volumes. The container goes first: Docker
// refuses to remove a volume a container still uses. A volume that can not be removed is reported, it does not fail
// the call, since the container is gone already.
func (s *server) remove(w http.ResponseWriter, r *http.Request) {
	c, ok := s.find(w, r)
	if !ok {
		return
	}
	removeVolumes := r.URL.Query().Get("removeVolumes") == "true"

	info, err := s.Engine.Inspect(r.Context(), c.ID)
	if err != nil {
		writeDockerError(w, err)
		return
	}
	if info.Running() || info.State == "restarting" || info.State == "paused" {
		writeError(w, http.StatusConflict, controllerapi.CodeConflict, fmt.Sprintf("the worker %s is %s, stop it first", c.WorkerID, info.State))
		return
	}
	if err := s.Engine.Remove(r.Context(), c.ID); err != nil {
		writeDockerError(w, err)
		return
	}
	s.Workers.Update(r.Context(), c.ID)

	resp := controllerapi.RemoveResponse{VolumesNotRemoved: []controllerapi.VolumeError{}}
	if removeVolumes {
		for _, name := range provision.VolumeNames(c.WorkerID) {
			if err := s.removeVolume(r.Context(), name, c.WorkerID); err != nil {
				resp.VolumesNotRemoved = append(resp.VolumesNotRemoved, controllerapi.VolumeError{Name: name, Error: err.Error()})
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// removeVolume removes a worker volume; one that is gone counts as removed, one that is not the worker's is left alone.
func (s *server) removeVolume(ctx context.Context, name, workerID string) error {
	labels, err := s.Engine.VolumeLabels(ctx, name)
	if errors.Is(err, docker.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if labels[workerapi.LabelID] != workerID {
		return fmt.Errorf("the volume does not carry the label %s=%s, it was left alone", workerapi.LabelID, workerID)
	}
	if err := s.Engine.VolumeRemove(ctx, name); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return nil
}

func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	c, ok := s.find(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	tail, err := strconv.Atoi(or(q.Get("tail"), "0"))
	if err != nil {
		writeError(w, http.StatusBadRequest, controllerapi.CodeInvalid, "tail must be a number")
		return
	}
	since := q.Get("since")
	if since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			writeError(w, http.StatusBadRequest, controllerapi.CodeInvalid, "since must be an RFC 3339 time")
			return
		}
	}
	opts := logs.Options{Tail: logs.Tail(tail), Since: since, Follow: q.Get("follow") == "true"}

	if !opts.Follow {
		lines := []controllerapi.LogLine{}
		err := logs.Stream(r.Context(), s.Engine, c.ID, opts, func(l controllerapi.LogLine) error {
			lines = append(lines, l)
			return nil
		})
		if err != nil {
			writeDockerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, lines)
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	err = logs.Stream(r.Context(), s.Engine, c.ID, opts, func(l controllerapi.LogLine) error {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		// the stream is open already, an error can only end it
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", "the log stream broke")
	}
}

// envFiles lists the names of the env files of the host, never their content.
func (s *server) envFiles(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.EnvDir)
	if err != nil {
		writeError(w, http.StatusBadGateway, controllerapi.CodeDocker, "failed to read the env directory")
		return
	}
	names := []string{}
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".env"); ok && e.Type().IsRegular() && filepath.Base(name) == name {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, names)
}

func (s *server) images(w http.ResponseWriter, r *http.Request) {
	imgs, err := s.Engine.ImageList(r.Context(), r.URL.Query().Get("reference"))
	if err != nil {
		writeDockerError(w, err)
		return
	}
	out := []controllerapi.Image{}
	for _, img := range imgs {
		tags := img.Tags
		if tags == nil {
			tags = []string{}
		}
		out = append(out, controllerapi.Image{ID: img.ID, Tags: tags, Created: img.Created, SizeBytes: img.SizeBytes})
	}
	writeJSON(w, http.StatusOK, out)
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
