package controllerclient

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
	mcapi "github.com/anovimous/automata-worker-monitor/internal/machinecontroller/api"
	mcconfig "github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/discovery"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/provision"
)

const token = "0123456789abcdef0123456789abcdef"

type noStats struct{}

func (noStats) Get(string) (controllerapi.Resources, bool) { return controllerapi.Resources{}, false }

type noScrapes struct{}

func (noScrapes) Get(string) (controllerapi.Telemetry, bool) { return controllerapi.Telemetry{}, false }

type noVolumes struct{}

func (noVolumes) Get(string) *controllerapi.Volumes { return nil }
func (noVolumes) Total() *int64                     { return nil }

type someHost struct{}

func (someHost) Latest() controllerapi.HostMetrics { return controllerapi.HostMetrics{CPUs: 8} }

type onePort struct{}

func (onePort) Allocate(map[int]bool) (int, error) { return 19100, nil }

// controller runs the real API of the worker machine controller on a fake Docker.
func controller(t *testing.T) (*httptest.Server, *docker.Fake) {
	t.Helper()
	envDir := t.TempDir()
	os.WriteFile(filepath.Join(envDir, "default.env"), []byte("X=1\n"), 0o600)
	f := docker.NewFake()
	f.AddImage("automata-worker:0.4.0")
	f.AddNetwork("automata_backend")
	disc := discovery.New(f)
	prov := provision.New(f, mcconfig.Config{EnvDir: envDir, AllowedImages: []string{"automata-worker:*"}}, onePort{}, disc, time.Now)
	ts := httptest.NewServer(mcapi.NewHandler(mcapi.Deps{
		HostName: "host1", Version: "0.1.0", Token: token, EnvDir: envDir, Engine: f, Workers: disc, Stats: noStats{},
		Scrapes: noScrapes{}, Volumes: noVolumes{}, Host: someHost{}, Provisioner: prov, Now: time.Now,
	}))
	t.Cleanup(ts.Close)
	return ts, f
}

func clientOf(t *testing.T, url, tok string) *Client {
	t.Helper()
	c, err := New(config.Host{Name: "host1", WorkerMachineControllerURL: url + "/", ControlToken: tok})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func createRequest(t *testing.T) controllerapi.CreateWorkerRequest {
	t.Helper()
	var req controllerapi.CreateWorkerRequest
	contracttest.DecodeStrict(t, contracttest.Read(t, "worker-machine-controller-create-worker.sample.json"), &req)
	return req
}

func TestAWorkerFromCreateToRemove(t *testing.T) {
	ts, f := controller(t)
	c := clientOf(t, ts.URL, token)
	ctx := context.Background()

	if h, err := c.Health(ctx); err != nil || h.HostName != "host1" {
		t.Fatalf("Health = %+v, %v", h, err)
	}
	created, err := c.Create(ctx, createRequest(t))
	if err != nil || created.WorkerID != "w-host1-k3f9qa" || created.ContainerID == "" {
		t.Fatalf("Create = %+v, %v", created, err)
	}
	snap, err := c.Snapshot(ctx)
	if err != nil || len(snap.Workers) != 1 || snap.Host.CPUs != 8 {
		t.Fatalf("Snapshot = %+v, %v", snap, err)
	}
	if d, err := c.Worker(ctx, "w-host1-k3f9qa"); err != nil || d.Details.ImageID == "" {
		t.Errorf("Worker = %+v, %v", d, err)
	}

	steps := []struct {
		name  string
		call  func() (controllerapi.WorkerObservation, error)
		state string
	}{
		{"drain", func() (controllerapi.WorkerObservation, error) { return c.Signal(ctx, "w-host1-k3f9qa", "SIGUSR1") }, "running"},
		{"stop", func() (controllerapi.WorkerObservation, error) { return c.Stop(ctx, "w-host1-k3f9qa", time.Minute) }, "exited"},
		{"start", func() (controllerapi.WorkerObservation, error) { return c.Start(ctx, "w-host1-k3f9qa") }, "running"},
		{"restart", func() (controllerapi.WorkerObservation, error) { return c.Restart(ctx, "w-host1-k3f9qa", time.Minute) }, "running"},
		{"kill", func() (controllerapi.WorkerObservation, error) { return c.Kill(ctx, "w-host1-k3f9qa") }, "exited"},
	}
	for _, s := range steps {
		o, err := s.call()
		if err != nil || o.Container.State != s.state {
			t.Fatalf("%s = %s, %v; want %s", s.name, o.Container.State, err, s.state)
		}
	}

	f.SetLogs(created.ContainerID, "2026-10-29T10:42:13Z hello\n", "")
	body, contentType, err := c.Logs(ctx, "w-host1-k3f9qa", LogOptions{Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(body)
	body.Close()
	if !strings.Contains(contentType, "application/json") || !strings.Contains(string(raw), "hello") {
		t.Errorf("logs = %s %s", contentType, raw)
	}

	rm, err := c.Remove(ctx, "w-host1-k3f9qa", true)
	if err != nil || len(rm.VolumesNotRemoved) != 0 || len(f.Volumes()) != 0 {
		t.Errorf("Remove = %+v, %v, volumes left %v", rm, err, f.Volumes())
	}
}

func TestErrorsAreTyped(t *testing.T) {
	ts, _ := controller(t)
	ctx := context.Background()

	_, err := clientOf(t, ts.URL, "wrong-token-0123456789abcdefghijkl").Snapshot(ctx)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Code != controllerapi.CodeUnauthorized {
		t.Errorf("wrong token = %v, want a 401 UNAUTHORIZED", err)
	}

	c := clientOf(t, ts.URL, token)
	if _, err := c.Start(ctx, "w-host1-nope00"); !IsNotFound(err) {
		t.Errorf("unknown worker = %v, want not found", err)
	}
	req := createRequest(t)
	req.Template.Image = "ubuntu:24.04"
	if _, err := c.Create(ctx, req); !errors.As(err, &ae) || ae.Code != controllerapi.CodeImageNotAllowed {
		t.Errorf("image not allowed = %v", err)
	}
	if IsNotFound(errors.New("other")) {
		t.Error("IsNotFound of a plain error is true")
	}
}

func TestAnAnswerThatIsNotTheAPI(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/snapshot" {
			w.WriteHeader(502)
			w.Write([]byte("Bad Gateway from a proxy"))
			return
		}
		w.Write([]byte("not json"))
	}))
	defer ts.Close()
	c := clientOf(t, ts.URL, token)

	var ae *APIError
	if _, err := c.Snapshot(context.Background()); !errors.As(err, &ae) || ae.Status != 502 || !strings.Contains(ae.Message, "proxy") {
		t.Errorf("proxy error = %v", err)
	}
	if _, err := c.Health(context.Background()); err == nil {
		t.Error("a body that is not JSON was accepted")
	}
}

func TestAnUnreachableController(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	if _, err := clientOf(t, url, token).Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "host1") {
		t.Errorf("err = %v, want one naming the host", err)
	}
}

func TestTLSWithTheHostsCA(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"hostName":"host1"}`))
	}))
	defer ts.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600)

	c, err := New(config.Host{Name: "host1", WorkerMachineControllerURL: ts.URL, ControlToken: token, CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	if h, err := c.Health(context.Background()); err != nil || h.HostName != "host1" {
		t.Errorf("Health over TLS = %+v, %v", h, err)
	}

	// without the CA the certificate is not trusted
	if _, err := clientOf(t, ts.URL, token).Health(context.Background()); err == nil {
		t.Error("a certificate of an unknown CA was accepted")
	}
}

func TestNewRefusesABadCAFile(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "x.pem")
	os.WriteFile(notPEM, []byte("nope"), 0o600)
	for _, ca := range []string{notPEM, filepath.Join(dir, "missing.pem")} {
		if _, err := New(config.Host{Name: "h", WorkerMachineControllerURL: "https://h:1", ControlToken: token, CAFile: ca}); err == nil {
			t.Errorf("New with %s returned no error", ca)
		}
	}
	if c := clientOf(t, "http://h:1", token); c.Host() != "host1" {
		t.Errorf("Host() = %s", c.Host())
	}
}

func TestImagesAndEnvFiles(t *testing.T) {
	ts, _ := controller(t)
	c := clientOf(t, ts.URL, token)
	if names, err := c.EnvFiles(context.Background()); err != nil || len(names) != 1 || names[0] != "default" {
		t.Errorf("EnvFiles = %v, %v", names, err)
	}
	if imgs, err := c.Images(context.Background(), "automata-worker"); err != nil || len(imgs) != 1 {
		t.Errorf("Images = %v, %v", imgs, err)
	}
}
