// Package provision creates and starts worker containers from a CreateWorkerRequest. The worker
// machine controller is root-equivalent on its host, so everything a request can ask for is checked: only allowed
// images, only named env files of the host, only the worker's own volumes and tmpfs (never a bind mount), no
// automata.* label from the template.
package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// Paths inside a worker container, and the telemetry port of a bridge-network worker.
const (
	SocketDir        = "/run/automata/sockets"
	TempDir          = "/var/lib/automata/tmp"
	WordlistCacheDir = "/var/cache/automata/wordlists"
	TelemetryPort    = 9464
)

// the stop timeout leaves the worker time to end its job and report it (5 s + up to 20 s + cleanup)
const (
	minStopTimeout = 45
	maxStopTimeout = 600
	minMemoryBytes = 256 << 20
	// a create is remembered until discovery shows its container, at most this long
	pendingFor = 5 * time.Minute
)

var (
	workerIDPattern     = regexp.MustCompile(`^w-[a-z0-9-]{1,40}-[a-z0-9]{6}$`)
	templateNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	envFilePattern      = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	networkPattern      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	labelKeyPattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./-]{0,127}$`)
	extraHostPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*:[a-zA-Z0-9.:_-]+$`)
)

// the env vars the controller sets itself; a template may not set them
var injectedKeys = map[string]bool{
	"WORKER_ID": true, "TELEMETRY_ADDR": true, "WORKER_SOCKET_DIR": true, "TEMP_DIR": true,
	"WORDLIST_CACHE_DIR": true, "WORDLIST_CACHE_MAX_BYTES": true, "LOG_FORMAT": true,
}

var restartPolicies = map[string]bool{"no": true, "on-failure": true, "unless-stopped": true, "always": true}

// Error is a create that failed, with the code of the API error.
type Error struct {
	Code        string // controllerapi.Code*
	Message     string
	ContainerID string // set when a container exists (CONFLICT, or a start that failed)
	Err         error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Err }

func invalid(format string, args ...any) *Error {
	return &Error{Code: controllerapi.CodeInvalid, Message: fmt.Sprintf(format, args...)}
}

func dockerError(msg string, err error) *Error {
	return &Error{Code: controllerapi.CodeDocker, Message: fmt.Sprintf("%s: %v", msg, err), Err: err}
}

// Workers lists the worker containers of the host (discovery.Discovery).
type Workers interface {
	Workers() []discovery.Container
}

type Provisioner struct {
	engine  docker.Engine
	cfg     config.Config
	ports   PortAllocator
	workers Workers
	now     func() time.Time

	// one create at a time: the worker id check and the port allocation hold until the container exists
	mu sync.Mutex
	// the creates discovery may not show yet, by worker id: their ids and ports are taken already
	pending map[string]pendingCreate
}

type pendingCreate struct {
	port int // 0 for a bridge worker
	at   time.Time
}

// PortAllocator gives a host-network worker its telemetry port (ports.Allocator).
type PortAllocator interface {
	Allocate(used map[int]bool) (int, error)
}

func New(engine docker.Engine, cfg config.Config, ports PortAllocator, workers Workers, now func() time.Time) *Provisioner {
	return &Provisioner{engine: engine, cfg: cfg, ports: ports, workers: workers, now: now, pending: map[string]pendingCreate{}}
}

// Create creates and starts a worker. A container that fails to start is left for its logs; the error names it.
func (p *Provisioner) Create(ctx context.Context, req controllerapi.CreateWorkerRequest) (controllerapi.CreateWorkerResponse, error) {
	if err := Validate(req); err != nil {
		return controllerapi.CreateWorkerResponse{}, err
	}
	t := req.Template
	if !p.cfg.ImageAllowed(t.Image) {
		return controllerapi.CreateWorkerResponse{}, &Error{Code: controllerapi.CodeImageNotAllowed,
			Message: fmt.Sprintf("the image %s is not allowed on this host", t.Image)}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	workers := p.workers.Workers()
	p.prunePending(workers)
	for _, w := range workers {
		if w.WorkerID == req.WorkerID {
			return controllerapi.CreateWorkerResponse{}, &Error{Code: controllerapi.CodeConflict, ContainerID: w.ID,
				Message: fmt.Sprintf("the worker %s exists already", req.WorkerID)}
		}
	}
	if _, ok := p.pending[req.WorkerID]; ok {
		return controllerapi.CreateWorkerResponse{}, &Error{Code: controllerapi.CodeConflict,
			Message: fmt.Sprintf("the worker %s is being created already", req.WorkerID)}
	}

	fileEnv, err := p.readEnvFile(t.EnvFile)
	if err != nil {
		return controllerapi.CreateWorkerResponse{}, err
	}
	if err := p.ensureImage(ctx, t.Image, t.PullPolicy); err != nil {
		return controllerapi.CreateWorkerResponse{}, err
	}
	if err := p.ensureNetwork(ctx, t.Network); err != nil {
		return controllerapi.CreateWorkerResponse{}, err
	}

	hostMode := t.Network == "host"
	port := TelemetryPort
	if hostMode {
		if port, err = p.ports.Allocate(p.usedPorts(workers)); err != nil {
			return controllerapi.CreateWorkerResponse{}, &Error{Code: controllerapi.CodeConflict, Message: err.Error()}
		}
	}

	volumeLabels := map[string]string{workerapi.LabelID: req.WorkerID, workerapi.LabelManagedBy: workerapi.ManagedByMonitor}
	for _, name := range VolumeNames(req.WorkerID) {
		if err := p.engine.VolumeCreate(ctx, name, volumeLabels); err != nil {
			return controllerapi.CreateWorkerResponse{}, dockerError("failed to create the volume "+name, err)
		}
	}

	spec := p.spec(req, fileEnv, port)
	id, err := p.engine.Create(ctx, spec)
	if err != nil {
		return controllerapi.CreateWorkerResponse{}, dockerError("failed to create the container", err)
	}
	// the container exists from here on, but discovery may show it only later: its id and port stay taken
	pc := pendingCreate{at: p.now()}
	if hostMode {
		pc.port = port
	}
	p.pending[req.WorkerID] = pc
	if err := p.engine.Start(ctx, id); err != nil {
		e := dockerError("the container was created but did not start", err)
		e.ContainerID = id
		return controllerapi.CreateWorkerResponse{}, e
	}

	resp := controllerapi.CreateWorkerResponse{WorkerID: req.WorkerID, ContainerID: id}
	if hostMode {
		resp.TelemetryURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	} else if info, err := p.engine.Inspect(ctx, id); err == nil && info.IP != "" {
		resp.TelemetryURL = fmt.Sprintf("http://%s:%d", info.IP, port)
	}
	return resp, nil
}

// VolumeNames are the two volumes of a worker: its wordlist cache and its work dir.
func VolumeNames(workerID string) []string {
	return []string{"automata-wl-" + workerID, "automata-tmp-" + workerID}
}

// Validate checks a request before anything is done with it.
func Validate(req controllerapi.CreateWorkerRequest) *Error {
	t := req.Template
	switch {
	case !workerIDPattern.MatchString(req.WorkerID):
		return invalid("workerId must match %s", workerIDPattern)
	case !templateNamePattern.MatchString(t.Name):
		return invalid("template.name must match %s", templateNamePattern)
	case t.Image == "" || strings.ContainsAny(t.Image, " \t\n"):
		return invalid("template.image is required")
	case t.PullPolicy != controllerapi.PullAlways && t.PullPolicy != controllerapi.PullIfMissing && t.PullPolicy != controllerapi.PullNever:
		return invalid("template.pullPolicy must be always, if-missing or never")
	case !envFilePattern.MatchString(t.EnvFile):
		return invalid("template.envFile must be a plain name matching %s", envFilePattern)
	case t.Network != "host" && !networkPattern.MatchString(t.Network):
		return invalid("template.network must be host or a network name")
	case t.CPUs <= 0:
		return invalid("template.cpus must be above 0")
	case t.MemoryBytes < minMemoryBytes:
		return invalid("template.memoryBytes must be at least %d", minMemoryBytes)
	case t.PidsLimit < 0:
		return invalid("template.pidsLimit must not be negative")
	case t.WordlistCacheMaxBytes <= 0:
		return invalid("template.wordlistCacheMaxBytes must be above 0")
	case t.TmpfsSocketsBytes <= 0:
		return invalid("template.tmpfsSocketsBytes must be above 0")
	case !restartPolicies[t.RestartPolicy.Name] || t.RestartPolicy.MaxRetries < 0:
		return invalid("template.restartPolicy must be no, on-failure, unless-stopped or always, with maxRetries at least 0")
	case t.RestartPolicy.MaxRetries > 0 && t.RestartPolicy.Name != "on-failure":
		return invalid("template.restartPolicy.maxRetries goes with on-failure only")
	case t.StopTimeoutSeconds < minStopTimeout || t.StopTimeoutSeconds > maxStopTimeout:
		return invalid("template.stopTimeoutSeconds must be from %d to %d", minStopTimeout, maxStopTimeout)
	}
	for key := range t.Env {
		if !envKeyPattern.MatchString(key) {
			return invalid("template.env key %q must match %s", key, envKeyPattern)
		}
		if injectedKeys[key] {
			return invalid("template.env may not set %s, the controller sets it", key)
		}
	}
	for _, h := range t.ExtraHosts {
		if !extraHostPattern.MatchString(h) {
			return invalid("template.extraHosts entry %q must be host:address", h)
		}
	}
	for key := range t.ExtraLabels {
		if strings.HasPrefix(key, "automata.") || !labelKeyPattern.MatchString(key) {
			return invalid("template.extraLabels key %q is not allowed (no automata. prefix)", key)
		}
	}
	return nil
}

func (p *Provisioner) readEnvFile(name string) (map[string]string, error) {
	f, err := os.Open(filepath.Join(p.cfg.EnvDir, name+".env"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, invalid("there is no env file %q on this host", name)
	}
	if err != nil {
		return nil, &Error{Code: controllerapi.CodeInvalid, Message: fmt.Sprintf("failed to open the env file %q", name), Err: err}
	}
	defer f.Close()
	env, err := ParseEnvFile(f)
	if err != nil {
		return nil, invalid("the env file %q is wrong: %v", name, err)
	}
	return env, nil
}

func (p *Provisioner) ensureImage(ctx context.Context, image, policy string) error {
	if policy == controllerapi.PullAlways {
		if err := p.engine.ImagePull(ctx, image); err != nil {
			return dockerError("failed to pull "+image, err)
		}
		return nil
	}
	exists, err := p.engine.ImageExists(ctx, image)
	if err != nil {
		return dockerError("failed to look for "+image, err)
	}
	switch {
	case exists:
		return nil
	case policy == controllerapi.PullNever:
		return invalid("the image %s is not on this host and the pull policy is never", image)
	}
	if err := p.engine.ImagePull(ctx, image); err != nil {
		return dockerError("failed to pull "+image, err)
	}
	return nil
}

// ensureNetwork accepts host and the existing networks; a missing network whose name starts with automata- is the
// controller's own and is created. Other networks are never created or changed.
func (p *Provisioner) ensureNetwork(ctx context.Context, name string) error {
	if name == "host" {
		return nil
	}
	exists, err := p.engine.NetworkExists(ctx, name)
	if err != nil {
		return dockerError("failed to look for the network "+name, err)
	}
	if exists {
		return nil
	}
	if !strings.HasPrefix(name, "automata-") {
		return invalid("there is no network %q on this host", name)
	}
	if err := p.engine.NetworkCreate(ctx, name, map[string]string{workerapi.LabelManagedBy: workerapi.ManagedByMonitor}); err != nil {
		return dockerError("failed to create the network "+name, err)
	}
	return nil
}

// prunePending forgets the creates that discovery shows now, and the old ones.
func (p *Provisioner) prunePending(workers []discovery.Container) {
	seen := map[string]bool{}
	for _, w := range workers {
		seen[w.WorkerID] = true
	}
	for id, pc := range p.pending {
		if seen[id] || p.now().Sub(pc.at) > pendingFor {
			delete(p.pending, id)
		}
	}
}

// usedPorts are the telemetry ports of the host-network workers, and those of the creates not shown yet.
func (p *Provisioner) usedPorts(workers []discovery.Container) map[int]bool {
	used := map[int]bool{}
	for _, pc := range p.pending {
		if pc.port != 0 {
			used[pc.port] = true
		}
	}
	for _, w := range workers {
		if w.Labels[workerapi.LabelNetworkMode] != "host" {
			continue
		}
		if port, err := strconv.Atoi(w.Labels[workerapi.LabelTelemetryPort]); err == nil {
			used[port] = true
		}
	}
	return used
}

func (p *Provisioner) spec(req controllerapi.CreateWorkerRequest, fileEnv map[string]string, port int) docker.CreateSpec {
	t := req.Template
	hostMode := t.Network == "host"

	env := map[string]string{}
	for k, v := range fileEnv {
		env[k] = v
	}
	for k, v := range t.Env {
		env[k] = v
	}
	telemetryAddr := fmt.Sprintf("0.0.0.0:%d", port)
	if hostMode {
		telemetryAddr = fmt.Sprintf("127.0.0.1:%d", port)
	}
	for k, v := range map[string]string{
		"WORKER_ID":                req.WorkerID,
		"TELEMETRY_ADDR":           telemetryAddr,
		"WORKER_SOCKET_DIR":        SocketDir,
		"TEMP_DIR":                 TempDir,
		"WORDLIST_CACHE_DIR":       WordlistCacheDir,
		"WORDLIST_CACHE_MAX_BYTES": strconv.FormatInt(t.WordlistCacheMaxBytes, 10),
		"LOG_FORMAT":               "json",
	} {
		env[k] = v
	}
	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	sort.Strings(envList)

	networkMode := "bridge"
	if hostMode {
		networkMode = "host"
	}
	labels := map[string]string{}
	for k, v := range t.ExtraLabels {
		labels[k] = v
	}
	labels[workerapi.LabelID] = req.WorkerID
	labels[workerapi.LabelManagedBy] = workerapi.ManagedByMonitor
	labels[workerapi.LabelTemplate] = t.Name
	labels[workerapi.LabelNetworkMode] = networkMode
	labels[workerapi.LabelTelemetryPort] = strconv.Itoa(port)
	labels[workerapi.LabelCreatedAt] = p.now().UTC().Format(time.RFC3339)

	names := VolumeNames(req.WorkerID)
	return docker.CreateSpec{
		Name:        "automata-worker-" + req.WorkerID,
		Image:       t.Image,
		Env:         envList,
		Labels:      labels,
		NetworkMode: t.Network,
		ExtraHosts:  t.ExtraHosts,
		NanoCPUs:    int64(t.CPUs * 1e9),
		MemoryBytes: t.MemoryBytes,
		PidsLimit:   t.PidsLimit,
		Restart:     docker.RestartPolicy{Name: t.RestartPolicy.Name, MaxRetries: t.RestartPolicy.MaxRetries},
		StopTimeout: t.StopTimeoutSeconds,
		// the worker creates cgroups for its jobs, which needs it today
		Privileged: true,
		Volumes: []docker.VolumeMount{
			{Name: names[0], Target: WordlistCacheDir},
			{Name: names[1], Target: TempDir},
		},
		// the worker does not create its socket directory, so it is the tmpfs mount point itself
		Tmpfs:      []docker.TmpfsMount{{Target: SocketDir, SizeBytes: t.TmpfsSocketsBytes, Mode: 0o700}},
		LogMaxSize: "50m",
		LogMaxFile: "3",
	}
}
