package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
)

var t0 = time.Date(2026, 10, 29, 9, 57, 58, 0, time.UTC)

// sampleRequest is the create request of the contract sample.
func sampleRequest(t *testing.T) controllerapi.CreateWorkerRequest {
	t.Helper()
	var req controllerapi.CreateWorkerRequest
	contracttest.DecodeStrict(t, contracttest.Read(t, "worker-machine-controller-create-worker.sample.json"), &req)
	return req
}

type staticWorkers []discovery.Container

func (s *staticWorkers) Workers() []discovery.Container { return *s }

type fixedPorts struct {
	next int
	used map[int]bool
}

func (f *fixedPorts) Allocate(used map[int]bool) (int, error) {
	f.used = used
	for p := f.next; p < f.next+100; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("full")
}

type setup struct {
	engine  *docker.Fake
	workers *staticWorkers
	ports   *fixedPorts
	p       *Provisioner
	envDir  string
}

func newSetup(t *testing.T) *setup {
	t.Helper()
	envDir := t.TempDir()
	envFile := "# credentials of the worker\nRABBITMQ_URL=amqp://automata:secret@rabbitmq:5672/\n" +
		"CENTRAL_POLL_INTERVAL_SECONDS=10\nWORKER_ID=from-the-file\n"
	if err := os.WriteFile(filepath.Join(envDir, "default.env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	f := docker.NewFake()
	f.AddImage("automata-worker:0.4.0")
	f.AddNetwork("automata_backend")
	s := &setup{engine: f, workers: &staticWorkers{}, ports: &fixedPorts{next: 19100}, envDir: envDir}
	cfg := config.Config{EnvDir: envDir, AllowedImages: []string{"automata-worker:*"}}
	s.p = New(f, cfg, s.ports, s.workers, func() time.Time { return t0 })
	return s
}

func envOf(spec docker.CreateSpec) map[string]string {
	env := map[string]string{}
	for _, kv := range spec.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}

func TestCreateABridgeWorker(t *testing.T) {
	s := newSetup(t)
	req := sampleRequest(t)

	resp, err := s.p.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if resp.WorkerID != "w-host1-k3f9qa" || resp.ContainerID == "" || !strings.HasSuffix(resp.TelemetryURL, ":9464") {
		t.Errorf("response = %+v", resp)
	}

	specs := s.engine.Created()
	if len(specs) != 1 {
		t.Fatalf("created %d containers, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Name != "automata-worker-w-host1-k3f9qa" || spec.Image != "automata-worker:0.4.0" || spec.NetworkMode != "automata_backend" {
		t.Errorf("spec = %+v", spec)
	}
	if spec.NanoCPUs != 2e9 || spec.MemoryBytes != 4294967296 || spec.PidsLimit != 512 || spec.StopTimeout != 60 || !spec.Privileged {
		t.Errorf("limits = %+v", spec)
	}
	if spec.Restart != (docker.RestartPolicy{Name: "on-failure", MaxRetries: 5}) || spec.LogMaxSize != "50m" || spec.LogMaxFile != "3" {
		t.Errorf("restart %+v logs %s/%s", spec.Restart, spec.LogMaxSize, spec.LogMaxFile)
	}

	env := envOf(spec)
	want := map[string]string{
		"WORKER_ID":                     "w-host1-k3f9qa", // the injected value wins over the env file's
		"TELEMETRY_ADDR":                "0.0.0.0:9464",
		"WORKER_SOCKET_DIR":             "/run/automata/sockets",
		"TEMP_DIR":                      "/var/lib/automata/tmp",
		"WORDLIST_CACHE_DIR":            "/var/cache/automata/wordlists",
		"WORDLIST_CACHE_MAX_BYTES":      "1610612736",
		"LOG_FORMAT":                    "json",
		"RABBITMQ_URL":                  "amqp://automata:secret@rabbitmq:5672/",
		"CENTRAL_POLL_INTERVAL_SECONDS": "5", // the template's value wins over the env file's
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env %s = %q, want %q", k, env[k], v)
		}
	}

	wantLabels := map[string]string{
		"automata.worker.id": "w-host1-k3f9qa", "automata.worker.managed-by": "automata-worker-monitor",
		"automata.worker.template": "default", "automata.worker.network-mode": "bridge",
		"automata.worker.telemetry-port": "9464", "automata.worker.created-at": "2026-10-29T09:57:58Z", "team": "red",
	}
	for k, v := range wantLabels {
		if spec.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, spec.Labels[k], v)
		}
	}
	if len(spec.Labels) != len(wantLabels) {
		t.Errorf("labels = %v, want exactly %v", spec.Labels, wantLabels)
	}

	if len(spec.Volumes) != 2 || spec.Volumes[0] != (docker.VolumeMount{Name: "automata-wl-w-host1-k3f9qa", Target: "/var/cache/automata/wordlists"}) ||
		spec.Volumes[1] != (docker.VolumeMount{Name: "automata-tmp-w-host1-k3f9qa", Target: "/var/lib/automata/tmp"}) {
		t.Errorf("volumes = %+v", spec.Volumes)
	}
	if len(spec.Tmpfs) != 1 || spec.Tmpfs[0] != (docker.TmpfsMount{Target: "/run/automata/sockets", SizeBytes: 16777216, Mode: 0o700}) {
		t.Errorf("tmpfs = %+v", spec.Tmpfs)
	}
	if got := s.engine.Volumes(); !slices.Equal(got, []string{"automata-tmp-w-host1-k3f9qa", "automata-wl-w-host1-k3f9qa"}) {
		t.Errorf("volumes created = %v", got)
	}
	if info, _ := s.engine.Inspect(context.Background(), resp.ContainerID); !info.Running() {
		t.Error("the container was not started")
	}
}

func TestCreateAHostWorkerGetsAFreePort(t *testing.T) {
	s := newSetup(t)
	*s.workers = staticWorkers{{WorkerID: "w-host1-aaaaaa", ContainerInfo: docker.ContainerInfo{Labels: map[string]string{
		"automata.worker.network-mode": "host", "automata.worker.telemetry-port": "19100"}}}}
	req := sampleRequest(t)
	req.Template.Network = "host"

	resp, err := s.p.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ports.used[19100] || resp.TelemetryURL != "http://127.0.0.1:19101" {
		t.Errorf("used ports %v, url %s, want 19100 skipped", s.ports.used, resp.TelemetryURL)
	}
	spec := s.engine.Created()[0]
	if spec.NetworkMode != "host" || spec.Labels["automata.worker.network-mode"] != "host" ||
		spec.Labels["automata.worker.telemetry-port"] != "19101" || envOf(spec)["TELEMETRY_ADDR"] != "127.0.0.1:19101" {
		t.Errorf("spec = %+v", spec)
	}
}

func TestValidate(t *testing.T) {
	tests := map[string]func(*controllerapi.CreateWorkerRequest){
		"worker id":              func(r *controllerapi.CreateWorkerRequest) { r.WorkerID = "compose-worker-1" },
		"template name":          func(r *controllerapi.CreateWorkerRequest) { r.Template.Name = "Default" },
		"no image":               func(r *controllerapi.CreateWorkerRequest) { r.Template.Image = "" },
		"pull policy":            func(r *controllerapi.CreateWorkerRequest) { r.Template.PullPolicy = "sometimes" },
		"env file path":          func(r *controllerapi.CreateWorkerRequest) { r.Template.EnvFile = "../../etc/shadow" },
		"env file dots":          func(r *controllerapi.CreateWorkerRequest) { r.Template.EnvFile = "default.env" },
		"network":                func(r *controllerapi.CreateWorkerRequest) { r.Template.Network = "bad network" },
		"cpus":                   func(r *controllerapi.CreateWorkerRequest) { r.Template.CPUs = 0 },
		"memory":                 func(r *controllerapi.CreateWorkerRequest) { r.Template.MemoryBytes = 1 << 20 },
		"pids":                   func(r *controllerapi.CreateWorkerRequest) { r.Template.PidsLimit = -1 },
		"cache":                  func(r *controllerapi.CreateWorkerRequest) { r.Template.WordlistCacheMaxBytes = 0 },
		"tmpfs":                  func(r *controllerapi.CreateWorkerRequest) { r.Template.TmpfsSocketsBytes = 0 },
		"restart policy":         func(r *controllerapi.CreateWorkerRequest) { r.Template.RestartPolicy.Name = "sometimes" },
		"retries without policy": func(r *controllerapi.CreateWorkerRequest) { r.Template.RestartPolicy.Name = "always" },
		"stop timeout short":     func(r *controllerapi.CreateWorkerRequest) { r.Template.StopTimeoutSeconds = 10 },
		"stop timeout long":      func(r *controllerapi.CreateWorkerRequest) { r.Template.StopTimeoutSeconds = 601 },
		"env key":                func(r *controllerapi.CreateWorkerRequest) { r.Template.Env = map[string]string{"bad-key": "x"} },
		"injected env key": func(r *controllerapi.CreateWorkerRequest) {
			r.Template.Env = map[string]string{"TELEMETRY_ADDR": "0.0.0.0:1"}
		},
		"extra host": func(r *controllerapi.CreateWorkerRequest) { r.Template.ExtraHosts = []string{"no address"} },
		"automata label": func(r *controllerapi.CreateWorkerRequest) {
			r.Template.ExtraLabels = map[string]string{"automata.worker.id": "x"}
		},
		"bad label key": func(r *controllerapi.CreateWorkerRequest) { r.Template.ExtraLabels = map[string]string{"": "x"} },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			req := sampleRequest(t)
			change(&req)
			err := Validate(req)
			if err == nil || err.Code != controllerapi.CodeInvalid {
				t.Errorf("Validate = %v, want INVALID", err)
			}
		})
	}
	if err := Validate(sampleRequest(t)); err != nil {
		t.Errorf("Validate(sample) = %v", err)
	}
}

func TestCreateRefusesAnImageThatIsNotAllowed(t *testing.T) {
	s := newSetup(t)
	req := sampleRequest(t)
	req.Template.Image = "ubuntu:24.04"

	_, err := s.p.Create(context.Background(), req)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != controllerapi.CodeImageNotAllowed || len(s.engine.Created()) != 0 {
		t.Errorf("err = %v, want IMAGE_NOT_ALLOWED and nothing created", err)
	}
}

func TestCreateRefusesAnExistingWorker(t *testing.T) {
	s := newSetup(t)
	*s.workers = staticWorkers{{WorkerID: "w-host1-k3f9qa", ContainerInfo: docker.ContainerInfo{ID: "c-old"}}}

	_, err := s.p.Create(context.Background(), sampleRequest(t))
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != controllerapi.CodeConflict || pe.ContainerID != "c-old" {
		t.Errorf("err = %v, want CONFLICT naming the container", err)
	}
}

func TestCreateNeedsTheEnvFile(t *testing.T) {
	s := newSetup(t)
	req := sampleRequest(t)
	req.Template.EnvFile = "staging"

	_, err := s.p.Create(context.Background(), req)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != controllerapi.CodeInvalid || !strings.Contains(pe.Message, "staging") {
		t.Errorf("err = %v, want INVALID naming the env file", err)
	}
}

func TestABadEnvFileDoesNotLeakItsValues(t *testing.T) {
	s := newSetup(t)
	os.WriteFile(filepath.Join(s.envDir, "broken.env"), []byte("PASSWORD hunter2\n"), 0o600)
	req := sampleRequest(t)
	req.Template.EnvFile = "broken"

	_, err := s.p.Create(context.Background(), req)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v, want an error without the value", err)
	}
}

func TestPullPolicies(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		policy     string
		local      bool
		wantPulled bool
		wantErr    bool
	}{
		{"always", true, true, false},
		{"if-missing", true, false, false},
		{"if-missing", false, true, false},
		{"never", true, false, false},
		{"never", false, false, true},
	}
	for _, tt := range tests {
		s := newSetup(t)
		req := sampleRequest(t)
		req.Template.Image = "automata-worker:0.5.0"
		req.Template.PullPolicy = tt.policy
		if tt.local {
			s.engine.AddImage("automata-worker:0.5.0")
		}

		_, err := s.p.Create(ctx, req)
		pulled := len(s.engine.Pulled()) == 1
		if (err != nil) != tt.wantErr || pulled != tt.wantPulled {
			t.Errorf("%s with the image local %v: err %v, pulled %v; want error %v, pulled %v", tt.policy, tt.local, err, pulled, tt.wantErr, tt.wantPulled)
		}
	}
}

func TestNetworks(t *testing.T) {
	ctx := context.Background()

	s := newSetup(t)
	req := sampleRequest(t)
	req.Template.Network = "someone_elses"
	if _, err := s.p.Create(ctx, req); err == nil {
		t.Error("a missing network that is not the controller's was accepted")
	}

	s = newSetup(t)
	req.Template.Network = "automata-workers"
	if _, err := s.p.Create(ctx, req); err != nil {
		t.Fatalf("Create on a new automata- network: %v", err)
	}
	if ok, _ := s.engine.NetworkExists(ctx, "automata-workers"); !ok {
		t.Error("the automata-workers network was not created")
	}
}

func TestAContainerThatDoesNotStartIsKept(t *testing.T) {
	s := newSetup(t)
	s.engine.SetErr("Start", errors.New("port is already allocated"))

	_, err := s.p.Create(context.Background(), sampleRequest(t))
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != controllerapi.CodeDocker || pe.ContainerID == "" {
		t.Fatalf("err = %v, want DOCKER naming the container", err)
	}
	if _, err := s.engine.Inspect(context.Background(), pe.ContainerID); err != nil {
		t.Error("the container that did not start was removed, its logs are lost")
	}
}

func TestDockerErrorsAreReported(t *testing.T) {
	for _, method := range []string{"VolumeCreate", "Create", "ImageExists", "NetworkExists"} {
		s := newSetup(t)
		s.engine.SetErr(method, errors.New("daemon broke"))
		_, err := s.p.Create(context.Background(), sampleRequest(t))
		var pe *Error
		if !errors.As(err, &pe) || pe.Code != controllerapi.CodeDocker {
			t.Errorf("%s failing: err = %v, want DOCKER", method, err)
		}
	}
}

func TestParseEnvFile(t *testing.T) {
	env, err := ParseEnvFile(strings.NewReader(`
# a comment
RABBITMQ_URL=amqp://u:p@h:5672/
export RUSTFS_ENDPOINT = rustfs:9000
QUOTED="a b # not a comment"
SINGLE='$HOME stays'
EMPTY=
EQUALS=a=b
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"RABBITMQ_URL": "amqp://u:p@h:5672/", "RUSTFS_ENDPOINT": "rustfs:9000",
		"QUOTED": "a b # not a comment", "SINGLE": "$HOME stays", "EMPTY": "", "EQUALS": "a=b"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("env = %v", env)
	}
}

func TestParseEnvFileRefusesBadLines(t *testing.T) {
	for _, content := range []string{"NO_EQUALS\n", "lower=x\n", "=x\n", "1ABC=x\n"} {
		if _, err := ParseEnvFile(strings.NewReader(content)); err == nil {
			t.Errorf("ParseEnvFile(%q) returned no error", content)
		}
	}
}

func TestVolumeNames(t *testing.T) {
	if got := VolumeNames("w-host1-k3f9qa"); !slices.Equal(got, []string{"automata-wl-w-host1-k3f9qa", "automata-tmp-w-host1-k3f9qa"}) {
		t.Errorf("VolumeNames = %v", got)
	}
}

func TestErrorUnwraps(t *testing.T) {
	inner := errors.New("inner")
	e := dockerError("outer", inner)
	if !errors.Is(e, inner) || !strings.Contains(e.Error(), "outer") {
		t.Errorf("error = %v", e)
	}
}

// discovery shows a new container only a moment later: two creates in a row must not get the same port
func TestBackToBackCreatesGetDifferentPorts(t *testing.T) {
	s := newSetup(t)
	first, second := sampleRequest(t), sampleRequest(t)
	first.Template.Network, second.Template.Network = "host", "host"
	second.WorkerID = "w-host1-zzzzzz"

	r1, err := s.p.Create(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.p.Create(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if r1.TelemetryURL == r2.TelemetryURL {
		t.Errorf("both workers got %s", r1.TelemetryURL)
	}
}

func TestACreateNotShownYetKeepsItsWorkerID(t *testing.T) {
	s := newSetup(t)
	if _, err := s.p.Create(context.Background(), sampleRequest(t)); err != nil {
		t.Fatal(err)
	}
	// discovery has not seen the container yet
	_, err := s.p.Create(context.Background(), sampleRequest(t))
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != controllerapi.CodeConflict {
		t.Errorf("err = %v, want CONFLICT for the same worker id", err)
	}
}

func TestPendingCreatesAreForgotten(t *testing.T) {
	s := newSetup(t)
	clock := t0
	s.p.now = func() time.Time { return clock }
	req := sampleRequest(t)
	req.Template.Network = "host"
	if _, err := s.p.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	// once discovery shows the worker, its pending entry goes
	*s.workers = staticWorkers{{WorkerID: req.WorkerID}}
	s.p.prunePending(s.workers.Workers())
	if len(s.p.pending) != 0 {
		t.Errorf("pending = %v after discovery showed the worker", s.p.pending)
	}

	// and an old one goes too
	s.p.pending["w-host1-old000"] = pendingCreate{port: 19150, at: clock}
	clock = clock.Add(pendingFor + time.Second)
	s.p.prunePending(nil)
	if len(s.p.pending) != 0 {
		t.Errorf("pending = %v after %s", s.p.pending, pendingFor)
	}
}
