package docker

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

func TestSummaryOf(t *testing.T) {
	s := summaryOf(container.Summary{ID: "abc", Names: []string{"/automata-worker-w-host1-k3f9qa"}, State: "running",
		Status: "Up 2 minutes (healthy)", Labels: map[string]string{"a": "b"}})

	if s.ID != "abc" || s.Name != "automata-worker-w-host1-k3f9qa" || s.State != "running" || s.Status != "Up 2 minutes (healthy)" || s.Labels["a"] != "b" {
		t.Errorf("summary = %+v", s)
	}
	if summaryOf(container.Summary{ID: "x"}).Name != "" {
		t.Error("a container without a name got one")
	}
}

func TestInfoOf(t *testing.T) {
	pids, stop := int64(512), 60
	info := infoOf(container.InspectResponse{
		ID: "abc", Name: "/automata-worker-w-1", Image: "sha256:img", Created: "2026-10-29T09:57:58.123Z", RestartCount: 2,
		State: &container.State{Status: "exited", ExitCode: 1, OOMKilled: true, StartedAt: "2026-10-29T09:58:01Z",
			FinishedAt: "0001-01-01T00:00:00Z", Health: &container.Health{Status: "unhealthy"}},
		Config: &container.Config{Image: "automata-worker:0.4.0", Labels: map[string]string{"automata.worker.id": "w-1"},
			Env: []string{"WORKER_ID=w-1", "RABBITMQ_URL=amqp://u:secret@h", "EMPTY="}, StopTimeout: &stop},
		HostConfig: &container.HostConfig{NetworkMode: "automata_backend",
			RestartPolicy: container.RestartPolicy{Name: "on-failure", MaximumRetryCount: 5},
			Resources:     container.Resources{NanoCPUs: 2e9, Memory: 4 << 30, PidsLimit: &pids}},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"zeta":             {IPAddress: netip.MustParseAddr("10.0.0.9")},
			"automata_backend": {IPAddress: netip.MustParseAddr("172.20.0.5")},
		}},
		Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "automata-wl-w-1", Destination: "/var/cache/automata/wordlists"}},
	})

	if info.ID != "abc" || info.Name != "automata-worker-w-1" || info.Image != "automata-worker:0.4.0" || info.ImageID != "sha256:img" {
		t.Errorf("identity = %+v", info)
	}
	if info.State != "exited" || info.ExitCode != 1 || !info.OOMKilled || info.Health != "unhealthy" || info.RestartCount != 2 {
		t.Errorf("state = %+v", info)
	}
	if !info.StartedAt.Equal(time.Date(2026, 10, 29, 9, 58, 1, 0, time.UTC)) || !info.FinishedAt.IsZero() || info.Created.IsZero() {
		t.Errorf("times started %s finished %s created %s", info.StartedAt, info.FinishedAt, info.Created)
	}
	if info.NetworkMode != "automata_backend" || info.IP != "172.20.0.5" {
		t.Errorf("network %s ip %s, want the address of the first network by name", info.NetworkMode, info.IP)
	}
	if info.NanoCPUs != 2e9 || info.MemoryBytes != 4<<30 || info.PidsLimit != 512 || *info.StopTimeout != 60 {
		t.Errorf("limits = %+v", info)
	}
	if len(info.EnvKeys) != 3 || info.EnvKeys[1] != "RABBITMQ_URL" {
		t.Errorf("env keys = %v, want the keys only", info.EnvKeys)
	}
	if info.Restart != (RestartPolicy{Name: "on-failure", MaxRetries: 5}) {
		t.Errorf("restart = %+v", info.Restart)
	}
	if len(info.Mounts) != 1 || info.Mounts[0] != (Mount{Type: "volume", Source: "automata-wl-w-1", Destination: "/var/cache/automata/wordlists"}) {
		t.Errorf("mounts = %+v", info.Mounts)
	}
	if !(ContainerInfo{State: "running"}).Running() || info.Running() {
		t.Error("Running() does not follow the state")
	}
}

func TestInfoOfAnEmptyInspect(t *testing.T) {
	// a container being created or removed may lack parts; nothing may panic
	info := infoOf(container.InspectResponse{ID: "x"})
	if info.ID != "x" || info.State != "" || info.IP != "" || info.PidsLimit != 0 {
		t.Errorf("info = %+v", info)
	}
}

func TestUnlimitedPidsIsZero(t *testing.T) {
	unlimited := int64(-1)
	info := infoOf(container.InspectResponse{HostConfig: &container.HostConfig{Resources: container.Resources{PidsLimit: &unlimited}}})
	if info.PidsLimit != 0 {
		t.Errorf("PidsLimit = %d, want 0 for no limit", info.PidsLimit)
	}
}

func TestCreateOptions(t *testing.T) {
	opts := createOptions(CreateSpec{
		Name: "automata-worker-w-1", Image: "automata-worker:0.4.0", Env: []string{"WORKER_ID=w-1"},
		Labels: map[string]string{"automata.worker.id": "w-1"}, NetworkMode: "host", ExtraHosts: []string{"h:host-gateway"},
		NanoCPUs: 2e9, MemoryBytes: 4 << 30, PidsLimit: 512, Restart: RestartPolicy{Name: "on-failure", MaxRetries: 5},
		StopTimeout: 60, Privileged: true,
		Volumes:    []VolumeMount{{Name: "automata-wl-w-1", Target: "/var/cache/automata/wordlists"}},
		Tmpfs:      []TmpfsMount{{Target: "/run/automata/sockets", SizeBytes: 16 << 20, Mode: 0o700}},
		LogMaxSize: "50m", LogMaxFile: "3",
	})

	hc, cfg := opts.HostConfig, opts.Config
	if opts.Name != "automata-worker-w-1" || cfg.Image != "automata-worker:0.4.0" || *cfg.StopTimeout != 60 || cfg.Labels["automata.worker.id"] != "w-1" {
		t.Errorf("config = %+v", cfg)
	}
	if !hc.Privileged || hc.CgroupnsMode != "private" || hc.Init == nil || *hc.Init || hc.NetworkMode != "host" {
		t.Errorf("host config = %+v", hc)
	}
	if hc.NanoCPUs != 2e9 || hc.Memory != 4<<30 || hc.MemorySwap != 4<<30 || *hc.PidsLimit != 512 {
		t.Errorf("resources = %+v, want no swap", hc.Resources)
	}
	if hc.RestartPolicy.Name != "on-failure" || hc.RestartPolicy.MaximumRetryCount != 5 {
		t.Errorf("restart = %+v", hc.RestartPolicy)
	}
	if hc.LogConfig.Type != "json-file" || hc.LogConfig.Config["max-size"] != "50m" || hc.LogConfig.Config["max-file"] != "3" {
		t.Errorf("logs = %+v", hc.LogConfig)
	}
	if len(hc.Binds) != 0 || len(hc.Mounts) != 2 {
		t.Fatalf("binds %v mounts %+v, want two mounts and no bind", hc.Binds, hc.Mounts)
	}
	if v := hc.Mounts[0]; v.Type != mount.TypeVolume || v.Source != "automata-wl-w-1" {
		t.Errorf("volume mount = %+v", v)
	}
	if tm := hc.Mounts[1]; tm.Type != mount.TypeTmpfs || tm.TmpfsOptions.SizeBytes != 16<<20 || tm.TmpfsOptions.Mode != 0o700 {
		t.Errorf("tmpfs mount = %+v", tm)
	}
}

func TestCreateOptionsWithoutAPidsLimit(t *testing.T) {
	if p := createOptions(CreateSpec{}).HostConfig.PidsLimit; p != nil {
		t.Errorf("PidsLimit = %d, want unset", *p)
	}
}

func TestSampleOf(t *testing.T) {
	read := time.Date(2026, 10, 29, 10, 42, 13, 0, time.UTC)
	s := sampleOf(container.StatsResponse{
		Read: read,
		CPUStats: container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 4812200000000},
			ThrottlingData: container.ThrottlingData{ThrottledTime: 12400000000}},
		MemoryStats: container.MemoryStats{Usage: 1003020288, Limit: 4294967296, Stats: map[string]uint64{"inactive_file": 190373888}},
		PidsStats:   container.PidsStats{Current: 9, Limit: 512},
		BlkioStats: container.BlkioStats{IoServiceBytesRecursive: []container.BlkioStatEntry{
			{Op: "read", Value: 10}, {Op: "Read", Value: 5}, {Op: "write", Value: 7}, {Op: "discard", Value: 99},
		}},
	})

	want := Sample{Read: read, CPUTotalNs: 4812200000000, CPUThrottledNs: 12400000000, MemUsage: 1003020288,
		MemInactiveFile: 190373888, MemLimit: 4294967296, Pids: 9, PidsLimit: 512, BlockRead: 15, BlockWrite: 7}
	if s != want {
		t.Errorf("sample = %+v\nwant     %+v", s, want)
	}
}

func TestEventOf(t *testing.T) {
	ev := eventOf(events.Message{Action: "die", Actor: events.Actor{ID: "abc"}, TimeNano: 1761734533000000000})
	if ev.ContainerID != "abc" || ev.Action != "die" || ev.Time.Unix() != 1761734533 {
		t.Errorf("event = %+v", ev)
	}
}

func TestWrapMapsTheDockerErrors(t *testing.T) {
	if wrap("x", nil) != nil {
		t.Error("wrap(nil) is not nil")
	}
	if err := wrap("inspect c1", cerrdefs.ErrNotFound); !errors.Is(err, ErrNotFound) {
		t.Errorf("not found = %v", err)
	}
	if err := wrap("remove c1", cerrdefs.ErrConflict); !errors.Is(err, ErrConflict) {
		t.Errorf("conflict = %v", err)
	}
	other := errors.New("boom")
	if err := wrap("start c1", other); !errors.Is(err, other) || errors.Is(err, ErrNotFound) || err.Error() != "docker start c1: boom" {
		t.Errorf("other = %v", err)
	}
}

func TestParseTime(t *testing.T) {
	if !parseTime("0001-01-01T00:00:00Z").IsZero() || !parseTime("").IsZero() {
		t.Error("Docker's never is not the zero time")
	}
	if got := parseTime("2026-10-29T09:58:01.5Z"); got.Nanosecond() != 5e8 {
		t.Errorf("parseTime = %s", got)
	}
}

func TestDemuxSplitsTheStreams(t *testing.T) {
	var src bytes.Buffer
	src.Write(Frame(1, []byte("out\n")))
	src.Write(Frame(2, []byte("err\n")))

	var out, errOut bytes.Buffer
	if err := Demux(&out, &errOut, &src); err != nil {
		t.Fatal(err)
	}
	if out.String() != "out\n" || errOut.String() != "err\n" {
		t.Errorf("stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestFakeContainerLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if _, err := f.Create(ctx, CreateSpec{Name: "w", Image: "missing:1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("create from a missing image = %v, want ErrNotFound", err)
	}
	f.AddImage("automata-worker:0.4.0")
	id, err := f.Create(ctx, CreateSpec{Name: "w", Image: "automata-worker:0.4.0", Labels: map[string]string{
		"automata.worker.id": "w-1", "automata.worker.managed-by": "automata-worker-monitor"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Create(ctx, CreateSpec{Name: "w", Image: "automata-worker:0.4.0"}); !errors.Is(err, ErrConflict) {
		t.Errorf("a second container with the same name = %v, want ErrConflict", err)
	}

	if err := f.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, id); !errors.Is(err, ErrConflict) {
		t.Errorf("remove while running = %v, want ErrConflict", err)
	}
	if err := f.Kill(ctx, id, "SIGUSR1"); err != nil {
		t.Fatal(err)
	}
	if info, _ := f.Inspect(ctx, id); !info.Running() {
		t.Error("SIGUSR1 stopped the fake container")
	}
	if err := f.Stop(ctx, id, time.Second); err != nil {
		t.Fatal(err)
	}
	if info, _ := f.Inspect(ctx, id); info.State != "exited" || info.ExitCode != 0 {
		t.Errorf("after stop = %s %d", info.State, info.ExitCode)
	}
	if list, _ := f.ListWorkerContainers(ctx); len(list) != 1 {
		t.Errorf("list = %+v, want the managed container", list)
	}
	if err := f.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Inspect(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("inspect after remove = %v", err)
	}
}

func TestFakeListsOnlyManagedContainers(t *testing.T) {
	f := NewFake()
	f.AddContainer(ContainerInfo{ID: "a", Labels: map[string]string{"automata.worker.id": "compose-1"}}, "Up")
	f.AddContainer(ContainerInfo{ID: "b", Labels: map[string]string{"automata.worker.id": "w-1", "automata.worker.managed-by": "automata-worker-monitor"}}, "Up")

	list, _ := f.ListWorkerContainers(context.Background())
	if len(list) != 1 || list[0].ID != "b" {
		t.Errorf("list = %+v, want only the managed one", list)
	}
}

func TestFakeErrs(t *testing.T) {
	f := NewFake()
	boom := errors.New("boom")
	f.SetErr("Ping", boom)
	if _, err := f.Ping(context.Background()); err != boom {
		t.Errorf("Ping = %v, want the injected error", err)
	}
}

func TestFrameIsWhatStdCopyReads(t *testing.T) {
	var out, errOut bytes.Buffer
	src := append(Frame(2, []byte("e")), Frame(1, []byte("o"))...)
	if err := Demux(&out, &errOut, bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	if out.String() != "o" || errOut.String() != "e" {
		t.Errorf("stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestFakeLogsAreMultiplexed(t *testing.T) {
	f := NewFake()
	f.AddContainer(ContainerInfo{ID: "c"}, "Up")
	f.SetLogs("c", "a\nb\n", "c\n")

	rc, err := f.Logs(context.Background(), "c", LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	var out, errOut bytes.Buffer
	if err := Demux(&out, &errOut, rc); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a\nb\n" || errOut.String() != "c\n" {
		t.Errorf("stdout %q stderr %q", out.String(), errOut.String())
	}
}
