package controllerapi

import (
	"encoding/json"
	"testing"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

func TestSnapshotSampleRoundTrips(t *testing.T) {
	var s Snapshot
	contracttest.AssertRoundTrip(t, "worker-machine-controller-snapshot.sample.json", &s)
}

func TestCreateWorkerSampleRoundTrips(t *testing.T) {
	var r CreateWorkerRequest
	contracttest.AssertRoundTrip(t, "worker-machine-controller-create-worker.sample.json", &r)
}

func TestSnapshotSampleIsRead(t *testing.T) {
	var s Snapshot
	contracttest.DecodeStrict(t, contracttest.Read(t, "worker-machine-controller-snapshot.sample.json"), &s)

	if s.HostName != "host1" || len(s.Workers) != 2 {
		t.Fatalf("snapshot of %s with %d workers, want host1 with 2", s.HostName, len(s.Workers))
	}
	if s.Host.VolumesBytes == nil || *s.Host.VolumesBytes != 1054871552 {
		t.Errorf("host volumesBytes = %v, want 1054871552", s.Host.VolumesBytes)
	}

	running := s.Workers[0]
	if running.Container.Labels[workerapi.LabelManagedBy] != workerapi.ManagedByMonitor || !running.Managed {
		t.Errorf("running worker is not managed by the monitor: %+v", running.Container.Labels)
	}
	if running.Resources == nil || running.Resources.NetRxBytesPerSec == nil || *running.Resources.NetRxBytesPerSec != 1250000 {
		t.Errorf("running worker resources = %+v, want net rx 1250000 B/s", running.Resources)
	}
	if running.Volumes == nil || len(running.Volumes.Items) != 2 || running.Volumes.Items[1].MountPath != "/var/lib/automata/tmp" {
		t.Errorf("running worker volumes = %+v, want the cache and the work dir", running.Volumes)
	}
	if st := running.Telemetry.Status; st == nil || st.Job == nil || st.Job.JobID != 4211 {
		t.Errorf("running worker status = %+v, want job 4211", st)
	}

	exited := s.Workers[1]
	if exited.Resources != nil || exited.Telemetry.Status != nil || exited.Container.ExitCode != 1 {
		t.Errorf("exited worker = %+v, want no resources, no status, exit code 1", exited)
	}
	if exited.Container.Limits.CPUs == nil || *exited.Container.Limits.CPUs != 2 {
		t.Errorf("exited worker cpus limit = %v, want 2", exited.Container.Limits.CPUs)
	}
}

// The worker status inside a snapshot must stay equal to the worker's own sample, so that the two contracts agree.
func TestSnapshotCarriesTheWorkerStatusSample(t *testing.T) {
	var s Snapshot
	contracttest.DecodeStrict(t, contracttest.Read(t, "worker-machine-controller-snapshot.sample.json"), &s)
	got, err := json.Marshal(s.Workers[0].Telemetry.Status)
	if err != nil {
		t.Fatal(err)
	}

	same, err := contracttest.SameJSON(got, contracttest.Read(t, "worker-status.v1.running.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Error("the status in the snapshot sample differs from worker-status.v1.running.json")
	}
}

func TestErrorResponseShape(t *testing.T) {
	b, err := json.Marshal(ErrorResponse{Error: ErrorBody{Code: CodeNotFound, Message: "no worker w-x"}})
	if err != nil {
		t.Fatal(err)
	}
	same, err := contracttest.SameJSON(b, []byte(`{"error": {"code": "NOT_FOUND", "message": "no worker w-x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Errorf("error body = %s", b)
	}
}

func TestWorkerDetailFlattensTheObservation(t *testing.T) {
	b, err := json.Marshal(WorkerDetail{WorkerObservation: WorkerObservation{WorkerID: "w-host1-k3f9qa"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["workerId"] != "w-host1-k3f9qa" || doc["details"] == nil {
		t.Errorf("worker detail = %s, want workerId at the top and a details object", b)
	}
}
