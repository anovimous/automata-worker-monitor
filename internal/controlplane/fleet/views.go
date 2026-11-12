package fleet

import (
	"sort"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/lifecycle"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// WorkerView is a worker as the API shows it. The parts of the delayed and rejected features are left
// out: the dependencies card and the heartbeat age (H4, H5), the job CPU (R5), the network rates (R3), the job link
// (A4).
type WorkerView struct {
	ID              string     `json:"id"`
	Host            string     `json:"host"`
	Managed         bool       `json:"managed"`
	Template        string     `json:"template"`
	Image           string     `json:"image"`
	Version         string     `json:"version"`
	ContainerID     string     `json:"containerId"`
	Lifecycle       string     `json:"lifecycle"`
	LifecycleReason string     `json:"lifecycleReason"`
	LifecycleSince  time.Time  `json:"lifecycleSince"`
	DrainDeadline   *time.Time `json:"drainDeadline"`
	Health          string     `json:"health"`
	HealthReasons   []string   `json:"healthReasons"`
	HealthMessage   string     `json:"healthMessage"`
	HealthSince     time.Time  `json:"healthSince"`
	Responsive      bool       `json:"responsive"`
	LastSeenAt      *time.Time `json:"lastSeenAt"`
	UptimeSeconds   *int64     `json:"uptimeSeconds"`
	RestartCount    int        `json:"restartCount"`
	LastExitCode    *int       `json:"lastExitCode"`
	OOMKills        int        `json:"oomKills"`

	Activity      *Activity              `json:"activity"`
	Resources     *Resources             `json:"resources"`
	WordlistCache *WordlistCache         `json:"wordlistCache"`
	Volumes       *controllerapi.Volumes `json:"volumes"`
	WorkDirBytes  *int64                 `json:"workDirBytes"`
	Counters      *workerapi.Counters    `json:"counters"`
	Limits        controllerapi.Limits   `json:"limits"`
	CreatedAt     time.Time              `json:"createdAt"`

	AllowedActions []string `json:"allowedActions"`
}

// Activity is what the worker is busy with.
type Activity struct {
	Phase              string    `json:"phase"`
	PhaseDetail        string    `json:"phaseDetail"`
	PhaseSince         time.Time `json:"phaseSince"`
	ProgressAgeSeconds int       `json:"progressAgeSeconds"`
	PhaseBudgetSeconds int       `json:"phaseBudgetSeconds"`
	Draining           bool      `json:"draining"`
	ShuttingDown       bool      `json:"shuttingDown"`
	Job                *JobInfo  `json:"job"`
	StatusAt           time.Time `json:"statusAt"`
	Stale              bool      `json:"stale"` // the last status read, not a live one
}

type JobInfo struct {
	JobID      int64      `json:"jobId"`
	RoutineKey string     `json:"routineKey"`
	Command    string     `json:"command"`
	State      string     `json:"state"`
	TakenAt    time.Time  `json:"takenAt"`
	StartedAt  *time.Time `json:"startedAt"`
}

type Resources struct {
	CPUCores              float64  `json:"cpuCores"`
	CPULimit              *float64 `json:"cpuLimit"`
	MemoryWorkingSetBytes int64    `json:"memoryWorkingSetBytes"`
	MemoryLimitBytes      *int64   `json:"memoryLimitBytes"`
	Pids                  int64    `json:"pids"`
	PidsLimit             *int64   `json:"pidsLimit"`
	BlockReadBytesPerSec  *float64 `json:"blockReadBytesPerSec"`
	BlockWriteBytesPerSec *float64 `json:"blockWriteBytesPerSec"`
}

type WordlistCache struct {
	Bytes    int64 `json:"bytes"`
	MaxBytes int64 `json:"maxBytes"`
	Files    int   `json:"files"`
}

func (f *Fleet) viewOf(w *worker) WorkerView {
	now := f.now()
	r := w.row
	v := WorkerView{
		ID: r.ID, Host: r.Host, Managed: r.Managed, Version: r.Version, ContainerID: r.ContainerID,
		Lifecycle: r.Lifecycle, LifecycleReason: r.LifecycleReason, LifecycleSince: r.LifecycleSince, DrainDeadline: r.DrainDeadline,
		Health: string(w.res.Health), HealthReasons: w.res.Reasons, HealthMessage: w.res.Message, HealthSince: r.HealthSince,
		Responsive: w.res.Responsive, LastSeenAt: r.LastSeenAt, CreatedAt: r.CreatedAt,
	}
	if v.HealthReasons == nil {
		v.HealthReasons = []string{}
	}
	if w.tmpl != nil {
		v.Template, v.Image = w.tmpl.Name, w.tmpl.Image
	}
	cordoned := false
	if h, ok := f.hosts[r.Host]; ok {
		cordoned = h.cordoned
	}
	v.AllowedActions = lifecycle.Allowed(r.Lifecycle, cordoned, running(w))

	o := w.obs
	if o == nil {
		return v
	}
	c := o.Container
	if c.Image != "" {
		v.Image = c.Image
	}
	if v.Template == "" {
		v.Template = o.Template
	}
	v.RestartCount, v.OOMKills, v.Limits = c.RestartCount, c.OOMEvents, c.Limits
	if c.OOMKilled && v.OOMKills == 0 {
		v.OOMKills = 1
	}
	if c.State == "running" && c.StartedAt != nil {
		up := int64(now.Sub(*c.StartedAt).Seconds())
		v.UptimeSeconds = &up
	} else if c.State == "exited" || c.State == "dead" {
		code := c.ExitCode
		v.LastExitCode = &code
	}

	if res := o.Resources; res != nil {
		v.Resources = &Resources{CPUCores: res.CPUCores, CPULimit: c.Limits.CPUs, MemoryWorkingSetBytes: res.MemoryWorkingSetBytes,
			MemoryLimitBytes: c.Limits.MemoryBytes, Pids: res.Pids, PidsLimit: res.PidsLimit,
			BlockReadBytesPerSec: w.blkReadRate, BlockWriteBytesPerSec: w.blkWriteRate}
	}
	if vols := o.Volumes; vols != nil {
		v.Volumes = vols
		for _, item := range vols.Items {
			if item.MountPath == "/var/lib/automata/tmp" {
				v.WorkDirBytes = item.Bytes
			}
		}
	}
	if s := o.Telemetry.Status; s != nil && c.State == "running" {
		v.Activity = activityOf(s, !o.Telemetry.OK)
		if wc := s.WordlistCache; wc != nil {
			v.WordlistCache = &WordlistCache{Bytes: wc.Bytes, MaxBytes: wc.MaxBytes, Files: wc.Files}
		}
		counters := s.Counters
		v.Counters = &counters
	}
	return v
}

func activityOf(s *workerapi.Status, stale bool) *Activity {
	a := &Activity{Phase: string(s.Phase), PhaseDetail: s.PhaseDetail, PhaseSince: s.PhaseSince,
		ProgressAgeSeconds: int(s.ProgressAge().Seconds()), PhaseBudgetSeconds: s.PhaseBudgetSeconds,
		Draining: s.Draining, ShuttingDown: s.ShuttingDown, StatusAt: s.Now, Stale: stale}
	if j := s.Job; j != nil {
		a.Job = &JobInfo{JobID: j.JobID, RoutineKey: j.RoutineKey, Command: j.Command, State: j.State, TakenAt: j.TakenAt, StartedAt: j.StartedAt}
	}
	return a
}

// Workers returns the views of the workers, ordered by id.
func (f *Fleet) Workers() []WorkerView {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]WorkerView, 0, len(f.workers))
	for _, w := range f.workers {
		out = append(out, f.viewOf(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Worker returns the view of one worker.
func (f *Fleet) Worker(id string) (WorkerView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[id]
	if !ok {
		return WorkerView{}, false
	}
	return f.viewOf(w), true
}

// TemplateSnapshot returns the template a worker was created from, as stored with it.
func (f *Fleet) TemplateSnapshot(id string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[id]
	if !ok {
		return nil, false
	}
	return w.row.TemplateSnapshot, true
}

// HostView is a host as the API shows it.
type HostView struct {
	Name                           string                     `json:"name"`
	URL                            string                     `json:"url"`
	Reachable                      bool                       `json:"reachable"`
	LastSeenAt                     *time.Time                 `json:"lastSeenAt"`
	Cordoned                       bool                       `json:"cordoned"`
	CordonedReason                 string                     `json:"cordonedReason"`
	WorkerMachineControllerVersion string                     `json:"workerMachineControllerVersion"`
	Metrics                        *controllerapi.HostMetrics `json:"metrics"`
	Workers                        HostWorkers                `json:"workers"`
	MaxWorkers                     int                        `json:"maxWorkers"`
	Capacity                       Capacity                   `json:"capacity"`
}

type HostWorkers struct {
	Total      int `json:"total"`
	Running    int `json:"running"`
	Responsive int `json:"responsive"`
	Busy       int `json:"busy"`
}

// Capacity compares the limits of the host's running workers with the host.
type Capacity struct {
	CPUsReserved        float64 `json:"cpusReserved"`
	MemoryBytesReserved int64   `json:"memoryBytesReserved"`
	CPUs                int     `json:"cpus"`
	MemoryBytes         int64   `json:"memoryBytes"`
}

func (f *Fleet) hostViewOf(h *host) HostView {
	v := HostView{Name: h.cfg.Name, URL: h.cfg.WorkerMachineControllerURL, Reachable: h.reachable, LastSeenAt: h.lastSeen,
		Cordoned: h.cordoned, CordonedReason: h.cordonedReason, WorkerMachineControllerVersion: h.controllerVersion,
		Metrics: h.metrics, MaxWorkers: h.cfg.MaxWorkers}
	for _, w := range f.workers {
		if w.row.Host != h.cfg.Name {
			continue
		}
		v.Workers.Total++
		if w.obs != nil && w.obs.Container.State == "running" {
			v.Workers.Running++
		}
		if w.res.Responsive {
			v.Workers.Responsive++
		}
		if busy(w) {
			v.Workers.Busy++
		}
	}
	v.Capacity.CPUsReserved, v.Capacity.MemoryBytesReserved = f.reserved(h.cfg.Name)
	if h.metrics != nil {
		v.Capacity.CPUs, v.Capacity.MemoryBytes = h.metrics.CPUs, h.metrics.MemoryTotalBytes
	}
	return v
}

// running tells whether the container of a worker runs; without an observation yet it is taken as running, so that
// the actions stay offered (the worker machine controller refuses what Docker can not do).
func running(w *worker) bool {
	return w.obs == nil || w.obs.Container.State == "running"
}

// busy tells whether a worker works on a job (from PREPARING to FINISHING).
func busy(w *worker) bool {
	if w.obs == nil || !w.obs.Telemetry.OK || w.obs.Telemetry.Status == nil {
		return false
	}
	return w.obs.Telemetry.Status.Job != nil
}

// Hosts returns the views of the hosts, in the order of the configuration.
func (f *Fleet) Hosts() []HostView {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]HostView, 0, len(f.hosts))
	for _, hc := range f.cfg.Hosts {
		out = append(out, f.hostViewOf(f.hosts[hc.Name]))
	}
	return out
}

func (f *Fleet) Host(name string) (HostView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[name]
	if !ok {
		return HostView{}, false
	}
	return f.hostViewOf(h), true
}

// Overview is the summary of the fleet page.
type Overview struct {
	Workers     int            `json:"workers"`
	ByHealth    map[string]int `json:"byHealth"`
	ByLifecycle map[string]int `json:"byLifecycle"`
	Busy        int            `json:"busy"`
	Idle        int            `json:"idle"`
	Routines    map[string]int `json:"routines"` // routines running now, e.g. {"fuzz": 3}
	Hosts       []HostSummary  `json:"hosts"`
}

type HostSummary struct {
	Name      string `json:"name"`
	Reachable bool   `json:"reachable"`
	Cordoned  bool   `json:"cordoned"`
	Workers   int    `json:"workers"`
}

func (f *Fleet) Overview() Overview {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := Overview{ByHealth: map[string]int{}, ByLifecycle: map[string]int{}, Routines: map[string]int{}, Hosts: []HostSummary{}}
	perHost := map[string]int{}
	for _, w := range f.workers {
		o.Workers++
		o.ByHealth[string(w.res.Health)]++
		o.ByLifecycle[w.row.Lifecycle]++
		perHost[w.row.Host]++
		if busy(w) {
			o.Busy++
			o.Routines[w.obs.Telemetry.Status.Job.RoutineKey]++
		} else if w.res.Responsive {
			o.Idle++
		}
	}
	for _, hc := range f.cfg.Hosts {
		h := f.hosts[hc.Name]
		o.Hosts = append(o.Hosts, HostSummary{Name: hc.Name, Reachable: h.reachable, Cordoned: h.cordoned, Workers: perHost[hc.Name]})
	}
	return o
}

// Filter selects workers of the list (?host=&health=&lifecycle=&phase=&routine=&q=).
type Filter struct {
	Host, Health, Lifecycle, Phase, Routine, Q string
}

func (fl Filter) Match(v WorkerView) bool {
	switch {
	case fl.Host != "" && v.Host != fl.Host,
		fl.Health != "" && v.Health != fl.Health,
		fl.Lifecycle != "" && v.Lifecycle != fl.Lifecycle:
		return false
	case fl.Phase != "" && (v.Activity == nil || v.Activity.Phase != fl.Phase):
		return false
	case fl.Routine != "" && (v.Activity == nil || v.Activity.Job == nil || v.Activity.Job.RoutineKey != fl.Routine):
		return false
	case fl.Q != "" && !strings.Contains(strings.ToLower(v.ID+" "+v.ContainerID), strings.ToLower(fl.Q)):
		return false
	}
	return true
}

// LastPollAt is the time of the last snapshot of any host, for GET /healthz.
func (f *Fleet) LastPollAt() *time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	var last *time.Time
	for _, h := range f.hosts {
		if h.reachable && h.lastSeen != nil && (last == nil || h.lastSeen.After(*last)) {
			last = h.lastSeen
		}
	}
	return last
}
