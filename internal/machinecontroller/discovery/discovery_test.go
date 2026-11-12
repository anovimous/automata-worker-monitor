package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
)

func managed(workerID string, extra map[string]string) map[string]string {
	labels := map[string]string{
		"automata.worker.id":             workerID,
		"automata.worker.managed-by":     "automata-worker-monitor",
		"automata.worker.network-mode":   "bridge",
		"automata.worker.telemetry-port": "9464",
	}
	for k, v := range extra {
		labels[k] = v
	}
	return labels
}

func running(id, workerID string, created time.Time) docker.ContainerInfo {
	return docker.ContainerInfo{ID: id, Name: "automata-worker-" + workerID, State: "running", IP: "172.20.0.5",
		NetworkMode: "automata_backend", Created: created, Labels: managed(workerID, nil)}
}

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func TestRefreshFindsTheManagedContainers(t *testing.T) {
	f := docker.NewFake()
	f.AddContainer(running("c1", "w-host1-aaaaaa", t0), "Up 2 minutes")
	f.AddContainer(docker.ContainerInfo{ID: "c2", State: "running", Labels: map[string]string{"automata.worker.id": "compose-worker-1"}}, "Up")
	d := New(f)

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ws := d.Workers()
	if len(ws) != 1 || ws[0].WorkerID != "w-host1-aaaaaa" || ws[0].Status != "Up 2 minutes" || ws[0].TelemetryURL != "http://172.20.0.5:9464" {
		t.Errorf("workers = %+v, want only the managed one", ws)
	}
}

func TestRefreshDropsGoneContainersAndSeesChanges(t *testing.T) {
	ctx := context.Background()
	f := docker.NewFake()
	f.AddContainer(running("c1", "w-1", t0), "Up")
	f.AddContainer(running("c2", "w-2", t0), "Up")
	d := New(f)
	d.Refresh(ctx)

	f.SetContainer("c1", func(c *docker.ContainerInfo) { c.State, c.ExitCode = "exited", 1 })
	f.Remove(ctx, "c1") // c1 exited, so it can be removed
	f.SetContainer("c2", func(c *docker.ContainerInfo) { c.State, c.ExitCode = "exited", 1 })
	d.Refresh(ctx)

	ws := d.Workers()
	if len(ws) != 1 || ws[0].WorkerID != "w-2" || ws[0].State != "exited" || ws[0].ExitCode != 1 {
		t.Errorf("workers = %+v, want w-2 exited with 1", ws)
	}
}

func TestRefreshReturnsTheListError(t *testing.T) {
	f := docker.NewFake()
	boom := errors.New("daemon down")
	f.SetErr("ListWorkerContainers", boom)
	if err := New(f).Refresh(context.Background()); err != boom {
		t.Errorf("Refresh = %v, want the list error", err)
	}
}

func TestEventsKeepTheListFresh(t *testing.T) {
	ctx := context.Background()
	f := docker.NewFake()
	f.AddContainer(running("c1", "w-1", t0), "Up")
	d := New(f)
	d.Refresh(ctx)

	f.SetContainer("c1", func(c *docker.ContainerInfo) { c.State, c.ExitCode, c.OOMKilled = "exited", 137, true })
	d.Handle(ctx, docker.Event{ContainerID: "c1", Action: "oom"})
	d.Handle(ctx, docker.Event{ContainerID: "c1", Action: "die"})

	c, ok := d.Find("w-1")
	if !ok || c.State != "exited" || !c.OOMKilled || c.OOMEvents != 1 {
		t.Errorf("after oom and die: %+v", c)
	}

	d.Handle(ctx, docker.Event{ContainerID: "c1", Action: "destroy"})
	if _, ok := d.Find("w-1"); ok {
		t.Error("the container is still there after destroy")
	}
}

func TestOOMEventsSurviveAReinspect(t *testing.T) {
	ctx := context.Background()
	f := docker.NewFake()
	f.AddContainer(running("c1", "w-1", t0), "Up")
	d := New(f)
	d.Refresh(ctx)
	d.Handle(ctx, docker.Event{ContainerID: "c1", Action: "oom"})
	d.Handle(ctx, docker.Event{ContainerID: "c1", Action: "oom"})
	d.Handle(ctx, docker.Event{ContainerID: "c1", Action: "restart"})

	if c, _ := d.Find("w-1"); c.OOMEvents != 2 {
		t.Errorf("oomEvents = %d, want 2", c.OOMEvents)
	}
}

func TestAnEventOfAnUnknownContainerAddsIt(t *testing.T) {
	ctx := context.Background()
	f := docker.NewFake()
	d := New(f)
	f.AddContainer(running("c9", "w-9", t0), "Up")
	d.Handle(ctx, docker.Event{ContainerID: "c9", Action: "create"})

	if _, ok := d.Find("w-9"); !ok {
		t.Error("the created container was not added")
	}
}

func TestUpdateDropsAContainerThatIsGone(t *testing.T) {
	ctx := context.Background()
	f := docker.NewFake()
	f.AddContainer(running("c1", "w-1", t0), "Up")
	d := New(f)
	d.Refresh(ctx)
	f.SetContainer("c1", func(c *docker.ContainerInfo) { c.State = "exited" })
	f.Remove(ctx, "c1")

	d.Update(ctx, "c1")
	if len(d.Workers()) != 0 {
		t.Error("the removed container is still listed")
	}
}

func TestDuplicateWorkerIDsAreFlagged(t *testing.T) {
	ctx := context.Background()
	f := docker.NewFake()
	f.AddContainer(running("c2", "w-1", t0.Add(time.Minute)), "Up")
	f.AddContainer(running("c1", "w-1", t0), "Up")
	f.AddContainer(running("c3", "w-3", t0), "Up")
	d := New(f)
	d.Refresh(ctx)

	ws := d.Workers()
	if len(ws) != 3 || !ws[0].Duplicate || !ws[1].Duplicate || ws[2].Duplicate {
		t.Errorf("workers = %+v, want both w-1 flagged", ws)
	}
	if c, _ := d.Find("w-1"); c.ID != "c1" {
		t.Errorf("Find(w-1) = %s, want the one created first", c.ID)
	}
}

func TestTelemetryURL(t *testing.T) {
	tests := []struct {
		name       string
		info       docker.ContainerInfo
		want       string
		configured bool
	}{
		{"bridge", docker.ContainerInfo{IP: "172.20.0.5", Labels: managed("w", nil)}, "http://172.20.0.5:9464", true},
		{"host mode label", docker.ContainerInfo{NetworkMode: "host", Labels: managed("w", map[string]string{
			"automata.worker.network-mode": "host", "automata.worker.telemetry-port": "19100"})}, "http://127.0.0.1:19100", true},
		{"host network without the label", docker.ContainerInfo{NetworkMode: "host", Labels: managed("w", nil)}, "http://127.0.0.1:9464", true},
		{"bridge without an address", docker.ContainerInfo{Labels: managed("w", nil)}, "", true},
		{"no telemetry port", docker.ContainerInfo{IP: "172.20.0.5", Labels: map[string]string{"automata.worker.id": "w"}}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, configured := telemetryURL(tt.info)
			if url != tt.want || configured != tt.configured {
				t.Errorf("telemetryURL = %q, %v, want %q, %v", url, configured, tt.want, tt.configured)
			}
		})
	}
}

func TestRunFollowsTheEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := docker.NewFake()
	f.AddImage("automata-worker:0.4.0")
	d := New(f)
	go d.Run(ctx, time.Hour)

	id, err := f.Create(ctx, docker.CreateSpec{Name: "automata-worker-w-7", Image: "automata-worker:0.4.0", Labels: managed("w-7", nil)})
	if err != nil {
		t.Fatal(err)
	}
	f.Start(ctx, id) // sends the start event

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := d.Find("w-7"); ok && c.Running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the started container was not seen through the events")
}

func TestRunReopensABrokenEventsStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := docker.NewFake()
	f.SetErr("Events", errors.New("stream broke"))
	d := New(f)
	done := make(chan struct{})
	go func() {
		d.Run(ctx, time.Hour)
		close(done)
	}()

	// the stream fails at once; after the first backoff (1 s) the refresh sees a container added meanwhile
	f.AddContainer(running("c1", "w-1", t0), "Up")
	f.SetErr("Events", nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := d.Find("w-1"); ok {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the container added during the break was not found")
}
