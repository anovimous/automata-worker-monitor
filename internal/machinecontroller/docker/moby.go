package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// the filter that keeps the worker containers created by the monitor; containers started by hand are not workers of
// the monitor (L10 is rejected), even with automata.worker.id
var workerLabel = workerapi.LabelManagedBy + "=" + workerapi.ManagedByMonitor

// Moby is the Engine of a real Docker daemon.
type Moby struct {
	cli *client.Client
}

// New connects to the daemon at host ("" is the Docker default, or DOCKER_HOST); the API version is negotiated on
// the first call.
func New(host string) (*Moby, error) {
	opts := []client.Opt{client.FromEnv}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create the Docker client: %w", err)
	}
	return &Moby{cli: cli}, nil
}

func (m *Moby) Close() error {
	return m.cli.Close()
}

func (m *Moby) Ping(ctx context.Context) (string, error) {
	res, err := m.cli.Ping(ctx, client.PingOptions{})
	if err != nil {
		return "", wrap("ping", err)
	}
	return res.APIVersion, nil
}

func (m *Moby) Info(ctx context.Context) (Info, error) {
	res, err := m.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Info{}, wrap("info", err)
	}
	version, err := m.cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return Info{}, wrap("version", err)
	}
	return Info{
		ServerVersion: res.Info.ServerVersion,
		APIVersion:    version.APIVersion,
		NCPU:          res.Info.NCPU,
		MemTotal:      res.Info.MemTotal,
		KernelVersion: res.Info.KernelVersion,
	}, nil
}

func (m *Moby) ListWorkerContainers(ctx context.Context) ([]ContainerSummary, error) {
	res, err := m.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", workerLabel)})
	if err != nil {
		return nil, wrap("list containers", err)
	}
	out := make([]ContainerSummary, 0, len(res.Items))
	for _, c := range res.Items {
		out = append(out, summaryOf(c))
	}
	return out, nil
}

func summaryOf(c container.Summary) ContainerSummary {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	return ContainerSummary{ID: c.ID, Name: name, Labels: c.Labels, State: string(c.State), Status: c.Status}
}

func (m *Moby) Inspect(ctx context.Context, id string) (ContainerInfo, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerInfo{}, wrap("inspect "+id, err)
	}
	return infoOf(res.Container), nil
}

func infoOf(c container.InspectResponse) ContainerInfo {
	info := ContainerInfo{
		ID:           c.ID,
		Name:         strings.TrimPrefix(c.Name, "/"),
		ImageID:      c.Image,
		Created:      parseTime(c.Created),
		RestartCount: c.RestartCount,
	}
	if s := c.State; s != nil {
		info.State = string(s.Status)
		info.ExitCode = s.ExitCode
		info.OOMKilled = s.OOMKilled
		info.StartedAt = parseTime(s.StartedAt)
		info.FinishedAt = parseTime(s.FinishedAt)
		if s.Health != nil {
			info.Health = string(s.Health.Status)
		}
	}
	if cfg := c.Config; cfg != nil {
		info.Image = cfg.Image
		info.Labels = cfg.Labels
		info.StopTimeout = cfg.StopTimeout
		for _, kv := range cfg.Env {
			key, _, _ := strings.Cut(kv, "=")
			info.EnvKeys = append(info.EnvKeys, key)
		}
	}
	if hc := c.HostConfig; hc != nil {
		info.NetworkMode = string(hc.NetworkMode)
		info.NanoCPUs = hc.NanoCPUs
		info.MemoryBytes = hc.Memory
		if hc.PidsLimit != nil && *hc.PidsLimit > 0 {
			info.PidsLimit = *hc.PidsLimit
		}
		info.Restart = RestartPolicy{Name: string(hc.RestartPolicy.Name), MaxRetries: hc.RestartPolicy.MaximumRetryCount}
	}
	if ns := c.NetworkSettings; ns != nil {
		info.IP = firstIP(ns.Networks)
	}
	for _, mp := range c.Mounts {
		info.Mounts = append(info.Mounts, Mount{Type: string(mp.Type), Source: mp.Name, Destination: mp.Destination})
	}
	return info
}

// firstIP is the address on the first network by name, so that it does not change between two inspects.
func firstIP(networks map[string]*network.EndpointSettings) string {
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if ep := networks[name]; ep != nil && ep.IPAddress.IsValid() {
			return ep.IPAddress.String()
		}
	}
	return ""
}

// parseTime reads a Docker time; Docker writes 0001-01-01T00:00:00Z for "never", which is the zero time.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (m *Moby) Create(ctx context.Context, spec CreateSpec) (string, error) {
	res, err := m.cli.ContainerCreate(ctx, createOptions(spec))
	if err != nil {
		return "", wrap("create "+spec.Name, err)
	}
	return res.ID, nil
}

func createOptions(spec CreateSpec) client.ContainerCreateOptions {
	stopTimeout := spec.StopTimeout
	noInit := false
	var pids *int64
	if spec.PidsLimit > 0 {
		p := spec.PidsLimit
		pids = &p
	}

	var mounts []mount.Mount
	for _, v := range spec.Volumes {
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: v.Name, Target: v.Target})
	}
	for _, t := range spec.Tmpfs {
		mounts = append(mounts, mount.Mount{Type: mount.TypeTmpfs, Target: t.Target,
			TmpfsOptions: &mount.TmpfsOptions{SizeBytes: t.SizeBytes, Mode: os.FileMode(t.Mode)}})
	}

	logConfig := map[string]string{}
	if spec.LogMaxSize != "" {
		logConfig["max-size"] = spec.LogMaxSize
	}
	if spec.LogMaxFile != "" {
		logConfig["max-file"] = spec.LogMaxFile
	}

	return client.ContainerCreateOptions{
		Name: spec.Name,
		Config: &container.Config{
			Image:       spec.Image,
			Env:         spec.Env,
			Labels:      spec.Labels,
			StopTimeout: &stopTimeout,
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(spec.NetworkMode),
			ExtraHosts:    spec.ExtraHosts,
			Privileged:    spec.Privileged,
			CgroupnsMode:  container.CgroupnsMode("private"),
			Init:          &noInit,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(spec.Restart.Name), MaximumRetryCount: spec.Restart.MaxRetries},
			LogConfig:     container.LogConfig{Type: "json-file", Config: logConfig},
			Resources: container.Resources{
				NanoCPUs:   spec.NanoCPUs,
				Memory:     spec.MemoryBytes,
				MemorySwap: spec.MemoryBytes, // no swap
				PidsLimit:  pids,
			},
			Mounts: mounts,
		},
	}
}

func (m *Moby) Start(ctx context.Context, id string) error {
	_, err := m.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return wrap("start "+id, err)
}

func (m *Moby) Stop(ctx context.Context, id string, timeout time.Duration) error {
	secs := int(timeout.Seconds())
	_, err := m.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs})
	return wrap("stop "+id, err)
}

func (m *Moby) Kill(ctx context.Context, id string, signal string) error {
	_, err := m.cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: signal})
	return wrap("kill "+id, err)
}

func (m *Moby) Restart(ctx context.Context, id string, timeout time.Duration) error {
	secs := int(timeout.Seconds())
	_, err := m.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &secs})
	return wrap("restart "+id, err)
}

func (m *Moby) Remove(ctx context.Context, id string) error {
	_, err := m.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{})
	return wrap("remove "+id, err)
}

func (m *Moby) Stats(ctx context.Context, id string) (<-chan Sample, error) {
	res, err := m.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		return nil, wrap("stats "+id, err)
	}
	out := make(chan Sample)
	go func() {
		defer close(out)
		defer res.Body.Close()
		dec := json.NewDecoder(res.Body)
		for {
			var s container.StatsResponse
			if err := dec.Decode(&s); err != nil {
				return
			}
			select {
			case out <- sampleOf(s):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func sampleOf(s container.StatsResponse) Sample {
	sample := Sample{
		Read:            s.Read,
		CPUTotalNs:      s.CPUStats.CPUUsage.TotalUsage,
		CPUThrottledNs:  s.CPUStats.ThrottlingData.ThrottledTime,
		MemUsage:        s.MemoryStats.Usage,
		MemInactiveFile: s.MemoryStats.Stats["inactive_file"],
		MemLimit:        s.MemoryStats.Limit,
		Pids:            s.PidsStats.Current,
		PidsLimit:       s.PidsStats.Limit,
	}
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			sample.BlockRead += e.Value
		case "write":
			sample.BlockWrite += e.Value
		}
	}
	return sample
}

func (m *Moby) Events(ctx context.Context) (<-chan Event, <-chan error) {
	res := m.cli.Events(ctx, client.EventsListOptions{
		Filters: client.Filters{}.Add("type", string(events.ContainerEventType)).Add("label", workerLabel),
	})
	out := make(chan Event)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		for {
			select {
			case msg := <-res.Messages:
				select {
				case out <- eventOf(msg):
				case <-ctx.Done():
					return
				}
			case err := <-res.Err:
				errs <- wrap("events", err)
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, errs
}

func eventOf(msg events.Message) Event {
	return Event{ContainerID: msg.Actor.ID, Action: string(msg.Action), Time: time.Unix(0, msg.TimeNano)}
}

func (m *Moby) Logs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error) {
	rc, err := m.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Timestamps: opts.Timestamps, Follow: opts.Follow, Tail: opts.Tail, Since: opts.Since,
	})
	if err != nil {
		return nil, wrap("logs "+id, err)
	}
	return rc, nil
}

// Demux splits a log stream of Logs into stdout and stderr.
func Demux(stdout, stderr io.Writer, src io.Reader) error {
	_, err := stdcopy.StdCopy(stdout, stderr, src)
	return err
}

func (m *Moby) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := m.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, wrap("inspect image "+ref, err)
	}
	return true, nil
}

func (m *Moby) ImagePull(ctx context.Context, ref string) error {
	// the daemon's own registry credentials are used, nothing passes through the monitor
	res, err := m.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return wrap("pull "+ref, err)
	}
	defer res.Close()
	return wrap("pull "+ref, res.Wait(ctx))
}

func (m *Moby) ImageList(ctx context.Context, reference string) ([]Image, error) {
	opts := client.ImageListOptions{}
	if reference != "" {
		opts.Filters = client.Filters{}.Add("reference", reference)
	}
	res, err := m.cli.ImageList(ctx, opts)
	if err != nil {
		return nil, wrap("list images", err)
	}
	out := make([]Image, 0, len(res.Items))
	for _, img := range res.Items {
		out = append(out, Image{ID: img.ID, Tags: img.RepoTags, Created: time.Unix(img.Created, 0).UTC(), SizeBytes: img.Size})
	}
	return out, nil
}

func (m *Moby) NetworkExists(ctx context.Context, name string) (bool, error) {
	_, err := m.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, wrap("inspect network "+name, err)
	}
	return true, nil
}

func (m *Moby) NetworkCreate(ctx context.Context, name string, labels map[string]string) error {
	_, err := m.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: labels})
	return wrap("create network "+name, err)
}

func (m *Moby) VolumeCreate(ctx context.Context, name string, labels map[string]string) error {
	_, err := m.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels})
	return wrap("create volume "+name, err)
}

func (m *Moby) VolumeLabels(ctx context.Context, name string) (map[string]string, error) {
	res, err := m.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return nil, wrap("inspect volume "+name, err)
	}
	return res.Volume.Labels, nil
}

func (m *Moby) VolumeRemove(ctx context.Context, name string) error {
	_, err := m.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
	return wrap("remove volume "+name, err)
}

func (m *Moby) VolumeUsage(ctx context.Context) ([]VolumeUsage, error) {
	res, err := m.cli.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true, Verbose: true})
	if err != nil {
		return nil, wrap("disk usage", err)
	}
	out := make([]VolumeUsage, 0, len(res.Volumes.Items))
	for _, v := range res.Volumes.Items {
		size := int64(-1)
		if v.UsageData != nil {
			size = v.UsageData.Size
		}
		out = append(out, VolumeUsage{Name: v.Name, Labels: v.Labels, Size: size})
	}
	return out, nil
}

// wrap names the operation and maps Docker's not-found and conflict errors to ErrNotFound and ErrConflict.
func wrap(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("docker %s: %w: %w", op, ErrNotFound, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("docker %s: %w: %w", op, ErrConflict, err)
	default:
		return fmt.Errorf("docker %s: %w", op, err)
	}
}
