package volumes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
)

var t0 = time.Date(2026, 10, 29, 10, 41, 30, 0, time.UTC)

func workerLabels(id string) map[string]string {
	return map[string]string{"automata.worker.id": id, "automata.worker.managed-by": "automata-worker-monitor"}
}

func TestSampleGroupsTheVolumesByWorker(t *testing.T) {
	f := docker.NewFake()
	f.CreateVolume("automata-tmp-w-host1-k3f9qa", workerLabels("w-host1-k3f9qa"))
	f.CreateVolume("automata-wl-w-host1-k3f9qa", workerLabels("w-host1-k3f9qa"))
	f.CreateVolume("automata-wl-w-host1-p0m2zt", workerLabels("w-host1-p0m2zt"))
	f.CreateVolume("postgres_data", nil)
	f.CreateVolume("someone-else", workerLabels("w-host1-k3f9qa"))
	f.SetVolumeSize("automata-wl-w-host1-k3f9qa", 734003200)
	f.SetVolumeSize("automata-tmp-w-host1-k3f9qa", 52428800)
	f.SetVolumeSize("postgres_data", 1<<30)
	f.SetVolumeSize("someone-else", 1<<30)
	s := New(f, func() time.Time { return t0 })

	if s.Get("w-host1-k3f9qa") != nil || s.Total() != nil {
		t.Fatal("volumes before the first sample, want null")
	}
	if err := s.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}

	v := s.Get("w-host1-k3f9qa")
	if v == nil || len(v.Items) != 2 || !v.SampledAt.Equal(t0) {
		t.Fatalf("volumes = %+v, want the cache and the work dir", v)
	}
	if wl := v.Items[0]; wl.Name != "automata-wl-w-host1-k3f9qa" || wl.MountPath != "/var/cache/automata/wordlists" || *wl.Bytes != 734003200 {
		t.Errorf("cache volume = %+v", wl)
	}
	if tmp := v.Items[1]; tmp.Name != "automata-tmp-w-host1-k3f9qa" || tmp.MountPath != "/var/lib/automata/tmp" || *tmp.Bytes != 52428800 {
		t.Errorf("work dir volume = %+v", tmp)
	}
	if other := s.Get("w-host1-p0m2zt"); other == nil || len(other.Items) != 1 || other.Items[0].Bytes != nil {
		t.Errorf("volume without a size = %+v, want bytes null", other)
	}
	if total := s.Total(); total == nil || *total != 734003200+52428800 {
		t.Errorf("total = %v, want the two known worker volumes only", total)
	}
}

func TestGetReturnsACopy(t *testing.T) {
	f := docker.NewFake()
	f.CreateVolume("automata-wl-w-1", workerLabels("w-1"))
	s := New(f, time.Now)
	s.Sample(context.Background())

	s.Get("w-1").Items[0].Name = "changed"
	if s.Get("w-1").Items[0].Name != "automata-wl-w-1" {
		t.Error("changing a result changed the sampler")
	}
}

func TestATotalOfNoVolumesIsZero(t *testing.T) {
	s := New(docker.NewFake(), time.Now)
	s.Sample(context.Background())
	if total := s.Total(); total == nil || *total != 0 {
		t.Errorf("total = %v, want 0 after a sample", total)
	}
}

func TestSampleKeepsTheLastValuesOnAnError(t *testing.T) {
	f := docker.NewFake()
	f.CreateVolume("automata-wl-w-1", workerLabels("w-1"))
	s := New(f, time.Now)
	s.Sample(context.Background())

	f.SetErr("VolumeUsage", errors.New("daemon busy"))
	if err := s.Sample(context.Background()); err == nil {
		t.Error("Sample returned no error")
	}
	if s.Get("w-1") == nil {
		t.Error("the last values were dropped after a failed sample")
	}
}

func TestRunSamplesAtOnce(t *testing.T) {
	f := docker.NewFake()
	f.CreateVolume("automata-wl-w-1", workerLabels("w-1"))
	s := New(f, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx, time.Hour)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for s.Get("w-1") == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if s.Get("w-1") == nil {
		t.Error("Run did not sample at once")
	}
}
