package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/controllerclient"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/sse"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/store"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

func TestCreateThenActive(t *testing.T) {
	e := newEnv(t)
	res, err := e.f.Create(context.Background(), "host1", e.tmpl.ID, 2)
	if err != nil || len(res.Workers) != 2 || !res.Workers[0].OK || !res.Workers[1].OK || res.Workers[0].ID == res.Workers[1].ID {
		t.Fatalf("Create = %+v, %v", res, err)
	}
	id := res.Workers[0].ID
	if !strings.HasPrefix(id, "w-host1-") || len(id) != len("w-host1-")+6 {
		t.Errorf("worker id = %s", id)
	}
	v := e.view(id)
	if v.Lifecycle != "PROVISIONING" || v.Health != "STARTING" || v.ContainerID != "c-"+id || v.Template != "default" {
		t.Errorf("after create: %+v", v)
	}

	// the container runs but answers nothing yet: still starting
	e.poll(5 * time.Second)
	if v := e.view(id); v.Lifecycle != "PROVISIONING" || v.Health != "STARTING" {
		t.Errorf("before a status: %s %s %v", v.Lifecycle, v.Health, v.HealthReasons)
	}
	e.client.set(id, ready(e.later(5*time.Second)))
	e.poll(5 * time.Second)
	v = e.view(id)
	if v.Lifecycle != "ACTIVE" || v.Health != "HEALTHY" || !v.Responsive || v.Version != "0.4.0" || v.Activity == nil ||
		v.Activity.Phase != "IDLE" || v.Resources == nil || v.LastSeenAt == nil {
		t.Errorf("after a status: %+v", v)
	}
	if got := e.kinds(id); !slices.Equal(got, []string{"ACTION", "HEALTH_CHANGED", "LIFECYCLE_CHANGED", "HEALTH_CHANGED"}) &&
		!slices.Equal(got, []string{"ACTION", "LIFECYCLE_CHANGED", "HEALTH_CHANGED"}) {
		t.Errorf("events = %v", got)
	}

	// the row survives a restart of the control plane
	row, err := e.store.Worker(context.Background(), id)
	if err != nil || row.Lifecycle != "ACTIVE" || row.Health != "HEALTHY" || row.ContainerID != "c-"+id {
		t.Errorf("stored = %+v, %v", row, err)
	}
	var snapshot store.Template
	json.Unmarshal(row.TemplateSnapshot, &snapshot)
	if snapshot.Name != "default" || snapshot.Image != e.tmpl.Image {
		t.Errorf("template snapshot = %s", row.TemplateSnapshot)
	}
}

func TestTheCreateRequestIsTheTemplate(t *testing.T) {
	e := newEnv(t)
	res, _ := e.f.Create(context.Background(), "host1", e.tmpl.ID, 1)
	if calls := e.client.Calls(); len(calls) != 1 || calls[0] != "create "+res.Workers[0].ID {
		t.Errorf("calls = %v", calls)
	}
	spec := Spec(e.tmpl)
	if spec.RestartPolicy != (controllerapi.RestartPolicy{Name: "on-failure", MaxRetries: 5}) || spec.Image != e.tmpl.Image ||
		spec.StopTimeoutSeconds != 60 || spec.Env == nil || spec.ExtraLabels == nil || spec.ExtraHosts == nil {
		t.Errorf("spec = %+v", spec)
	}
	none := e.tmpl
	none.RestartMaxRetries = 0
	if Spec(none).RestartPolicy.Name != "no" {
		t.Error("no retries is not the policy no")
	}
}

func TestCreateRefusals(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	tests := []struct {
		name  string
		host  string
		tmpl  int64
		count int
		code  string
	}{
		{"count 0", "host1", e.tmpl.ID, 0, "INVALID"},
		{"count 21", "host1", e.tmpl.ID, 21, "INVALID"},
		{"unknown host", "host9", e.tmpl.ID, 1, "NOT_FOUND"},
		{"unknown template", "host1", 999, 1, "NOT_FOUND"},
		{"over max workers", "host1", e.tmpl.ID, 4, "MAX_WORKERS"},
	}
	for _, tt := range tests {
		_, err := e.f.Create(ctx, tt.host, tt.tmpl, tt.count)
		var fe *Error
		if !errors.As(err, &fe) || fe.Code != tt.code {
			t.Errorf("%s: err = %v, want %s", tt.name, err, tt.code)
		}
	}
	e.f.Cordon(ctx, "host1", true, "maintenance")
	_, err := e.f.Create(ctx, "host1", e.tmpl.ID, 1)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != "HOST_CORDONED" || fe.Status != 409 {
		t.Errorf("cordoned: err = %v", err)
	}
}

func TestAStoppedWorkerKeepsItsSlot(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.createActive()
	e.f.Act(ctx, id, "stop", ActionOptions{})
	e.poll(5 * time.Second)
	if _, err := e.f.Create(ctx, "host1", e.tmpl.ID, 3); err == nil {
		t.Error("3 more workers next to a stopped one went over maxWorkers 3")
	}
	if _, err := e.f.Create(ctx, "host1", e.tmpl.ID, 2); err != nil {
		t.Errorf("2 more: %v", err)
	}
}

func TestAFailedCreateIsProvisionFailed(t *testing.T) {
	e := newEnv(t)
	e.client.setErr("create", nil)
	e.client.errs = map[string]error{}
	failing := &controllerclient.APIError{Status: 502, Code: "DOCKER", Message: "the container was created but did not start: port in use (container c-x)"}
	e.f.random = strings.NewReader(strings.Repeat("a", 6))
	e.client.setErr("create w-host1-"+idOf("aaaaaa"), failing)

	res, err := e.f.Create(context.Background(), "host1", e.tmpl.ID, 1)
	if err != nil || res.Workers[0].OK || res.Workers[0].Error == "" {
		t.Fatalf("Create = %+v, %v", res, err)
	}
	v := e.view(res.Workers[0].ID)
	if v.Lifecycle != "PROVISION_FAILED" || v.ContainerID != "c-x" || !slices.Equal(v.AllowedActions, []string{"remove"}) {
		t.Errorf("view = %+v", v)
	}
	if !slices.Contains(e.kinds(v.ID), "PROVISION_FAILED") {
		t.Errorf("events = %v", e.kinds(v.ID))
	}
}

// idOf is the id drawn from six bytes of 'a' (the random source of the test).
func idOf(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = idLetters[int(b[i])%len(idLetters)]
	}
	return string(b)
}

func TestProvisionTimeout(t *testing.T) {
	e := newEnv(t)
	res, _ := e.f.Create(context.Background(), "host1", e.tmpl.ID, 1)
	id := res.Workers[0].ID
	e.poll(time.Minute)
	e.poll(time.Minute + time.Second)
	if v := e.view(id); v.Lifecycle != "PROVISION_FAILED" || v.LifecycleReason != "PROVISION_TIMEOUT" || v.Health != "NONE" {
		t.Errorf("after 121 s without a status: %s %s %s", v.Lifecycle, v.LifecycleReason, v.Health)
	}
}

func TestAnUnknownManagedContainerIsAdopted(t *testing.T) {
	e := newEnv(t)
	e.client.set("w-host1-zzzzzz", ready(t0.Add(5*time.Second)))
	e.poll(5 * time.Second)
	v := e.view("w-host1-zzzzzz")
	if v.Lifecycle != "ACTIVE" || v.Health != "HEALTHY" || !v.Managed || v.Template != "default" {
		t.Errorf("adopted = %+v", v)
	}
	if got := e.kinds("w-host1-zzzzzz"); got[0] != "ADOPTED" {
		t.Errorf("events = %v", got)
	}

	// an external container is not shown even if the controller sent it
	e.client.set("compose-worker-1", func(o *controllerapi.WorkerObservation) { o.Managed = false })
	e.poll(5 * time.Second)
	if _, ok := e.f.Worker("compose-worker-1"); ok {
		t.Error("an external container was adopted")
	}
}

func TestAStoppedContainerIsAdoptedStopped(t *testing.T) {
	e := newEnv(t)
	e.client.set("w-host1-yyyyyy", exited(0))
	e.poll(5 * time.Second)
	if v := e.view("w-host1-yyyyyy"); v.Lifecycle != "STOPPED" || v.Health != "NONE" {
		t.Errorf("adopted = %s %s", v.Lifecycle, v.Health)
	}
}

func TestAContainerRemovedByHandDisappears(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()
	updates, unsubscribe := e.broker.Subscribe()
	defer unsubscribe()
	e.client.drop(id)
	e.poll(5 * time.Second)

	if _, ok := e.f.Worker(id); ok {
		t.Error("the worker is still in the view")
	}
	row, _ := e.store.Worker(context.Background(), id)
	if row.Lifecycle != "REMOVED" || row.LifecycleReason != "DISAPPEARED" || row.RemovedAt == nil {
		t.Errorf("stored = %s %s %v", row.Lifecycle, row.LifecycleReason, row.RemovedAt)
	}
	if !slices.Contains(e.kinds(id), "DISAPPEARED") {
		t.Errorf("events = %v", e.kinds(id))
	}
	if !published(updates, "worker.removed") {
		t.Error("no worker.removed was pushed")
	}
}

func published(ch <-chan sse.Event, typ string) bool {
	for {
		select {
		case e := <-ch:
			if e.Type == typ {
				return true
			}
		default:
			return false
		}
	}
}

func TestRestartsAndOOMKills(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()

	e.client.set(id, func(o *controllerapi.WorkerObservation) { o.Container.State, o.Container.ExitCode = "exited", 1 })
	e.poll(5 * time.Second)
	if v := e.view(id); v.Health != "DOWN" || v.Lifecycle != "ACTIVE" || !slices.Contains(v.HealthReasons, "CONTAINER_EXITED") {
		t.Errorf("after the crash: %s %s %v", v.Lifecycle, v.Health, v.HealthReasons)
	}
	e.client.set(id, func(o *controllerapi.WorkerObservation) {
		ready(e.later(5 * time.Second))(o)
		o.Container.RestartCount, o.Container.OOMEvents = 1, 1
	})
	e.poll(5 * time.Second)
	e.client.set(id, ready(e.later(5*time.Second)))
	e.poll(5 * time.Second)

	kinds := e.kinds(id)
	if !slices.Contains(kinds, "CONTAINER_RESTARTED") || !slices.Contains(kinds, "OOM_KILLED") {
		t.Errorf("events = %v", kinds)
	}
	if v := e.view(id); v.Health != "DEGRADED" || !slices.Contains(v.HealthReasons, "RESTARTED_RECENTLY") || v.RestartCount != 1 || v.OOMKills != 1 {
		t.Errorf("after the restart: %s %v restarts %d oom %d", v.Health, v.HealthReasons, v.RestartCount, v.OOMKills)
	}
}

func TestJobEvents(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()
	job := func(jobID int64, finished int64) func(*controllerapi.WorkerObservation) {
		return func(o *controllerapi.WorkerObservation) {
			ready(e.later(5 * time.Second))(o)
			o.Telemetry.Status.Counters.JobsByOutcome = map[string]int64{"FINISHED": finished}
			if jobID != 0 {
				o.Telemetry.Status.Phase = workerapi.PhaseRunningJob
				o.Telemetry.Status.Job = &workerapi.Job{JobID: jobID, RoutineKey: "fuzz", State: "RUNNING"}
			}
		}
	}
	e.client.set(id, job(4211, 0))
	e.poll(5 * time.Second)
	if v := e.view(id); v.Activity.Job == nil || v.Activity.Job.JobID != 4211 {
		t.Errorf("activity = %+v", v.Activity)
	}
	ov := e.f.Overview()
	if ov.Busy != 1 || ov.Routines["fuzz"] != 1 {
		t.Errorf("overview = %+v", ov)
	}
	e.client.set(id, job(0, 1))
	e.poll(5 * time.Second)

	evs := e.events(id)
	if evs[0].Kind != "JOB_ENDED" || evs[0].From != "4211" || evs[0].To != "FINISHED" {
		t.Errorf("last event = %+v", evs[0])
	}
	if evs[1].Kind != "JOB_STARTED" || evs[1].To != "4211" || evs[1].Message != "fuzz" {
		t.Errorf("job started = %+v", evs[1])
	}
}

func TestDrainThenRemoveAfterDrain(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.tmpl.RemoveAfterDrain, e.tmpl.DrainTimeoutSeconds = true, 3600
	e.store.UpdateTemplate(ctx, e.tmpl, t0)
	id := e.createActive()

	res, err := e.f.Act(ctx, id, "drain", ActionOptions{})
	if err != nil || res.Worker.Lifecycle != "DRAINING" || res.Worker.DrainDeadline == nil ||
		!slices.Equal(res.Worker.AllowedActions, []string{"undrain", "stop", "kill"}) {
		t.Fatalf("drain = %+v, %v", res.Worker, err)
	}
	if !slices.Contains(e.client.Calls(), "signal:SIGUSR1 "+id) {
		t.Errorf("calls = %v", e.client.Calls())
	}

	e.client.set(id, exited(0))
	e.poll(5 * time.Second)
	e.f.Wait()
	if !slices.Contains(e.client.Calls(), "remove+volumes "+id) {
		t.Errorf("calls = %v, want the removal after the drain", e.client.Calls())
	}
	row, _ := e.store.Worker(ctx, id)
	if row.Lifecycle != "REMOVED" {
		t.Errorf("stored lifecycle = %s, want REMOVED", row.Lifecycle)
	}
	var reasons []string
	for _, ev := range e.events(id) {
		if ev.Kind == "LIFECYCLE_CHANGED" {
			reasons = append(reasons, ev.To+":"+strings.Join(ev.Reasons, ","))
		}
	}
	if !slices.Contains(reasons, "STOPPED:DRAINED") {
		t.Errorf("lifecycle changes = %v", reasons)
	}
}

func TestUndrain(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.tmpl.DrainTimeoutSeconds = 60
	e.store.UpdateTemplate(ctx, e.tmpl, t0)
	id := e.createActive()
	e.f.Act(ctx, id, "drain", ActionOptions{})

	res, err := e.f.Act(ctx, id, "undrain", ActionOptions{})
	if err != nil || res.Worker.Lifecycle != "ACTIVE" || res.Worker.DrainDeadline != nil {
		t.Errorf("undrain = %+v, %v", res.Worker, err)
	}
}

func TestDrainTimeout(t *testing.T) {
	for _, escalate := range []bool{false, true} {
		ctx := context.Background()
		e := newEnv(t)
		e.tmpl.DrainTimeoutSeconds, e.tmpl.EscalateAfterDrainTimeout = 60, escalate
		e.store.UpdateTemplate(ctx, e.tmpl, t0)
		id := e.createActive()
		e.f.Act(ctx, id, "drain", ActionOptions{})

		for range 3 {
			e.client.set(id, ready(e.later(30*time.Second)))
			e.poll(30 * time.Second)
		}
		e.f.Wait()
		timeouts := 0
		for _, k := range e.kinds(id) {
			if k == "DRAIN_TIMEOUT" {
				timeouts++
			}
		}
		if timeouts != 1 {
			t.Errorf("escalate %v: %d DRAIN_TIMEOUT events, want 1", escalate, timeouts)
		}
		stopped := slices.Contains(e.client.Calls(), "stop "+id)
		if stopped != escalate {
			t.Errorf("escalate %v: stop called %v", escalate, stopped)
		}
		v := e.view(id)
		if escalate && (v.Lifecycle != "STOPPING" || v.LifecycleReason != "DRAIN_TIMEOUT") {
			t.Errorf("escalated: %s %s", v.Lifecycle, v.LifecycleReason)
		}
		if !escalate && v.Lifecycle != "DRAINING" {
			t.Errorf("not escalated: %s", v.Lifecycle)
		}
	}
}

func TestAStopSeenDuringItsCallIsNotAnExitOfItsOwn(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.createActive()
	e.client.blockStop, e.client.stopped = make(chan struct{}), make(chan struct{})

	done := make(chan error, 1)
	go func() {
		_, err := e.f.Act(ctx, id, "stop", ActionOptions{TimeoutSeconds: 45})
		done <- err
	}()
	<-e.client.stopped
	// a poll while the stop call still runs sees the container exited with 0
	e.poll(5 * time.Second)
	close(e.client.blockStop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	e.poll(5 * time.Second)
	if v := e.view(id); v.Lifecycle != "STOPPED" || v.LifecycleReason != "" {
		t.Errorf("after the stop: %s %q, want STOPPED without EXITED_CLEANLY", v.Lifecycle, v.LifecycleReason)
	}
}

func TestActionRefusals(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.createActive()

	var fe *Error
	if _, err := e.f.Act(ctx, id, "start", ActionOptions{}); !errors.As(err, &fe) || fe.Code != "INVALID_TRANSITION" || fe.Status != 409 {
		t.Errorf("start of an active worker = %v", err)
	}
	if _, err := e.f.Act(ctx, "w-host1-nope00", "stop", ActionOptions{}); !errors.As(err, &fe) || fe.Status != 404 {
		t.Errorf("unknown worker = %v", err)
	}
	e.f.Act(ctx, id, "stop", ActionOptions{})
	e.poll(5 * time.Second)
	e.f.Cordon(ctx, "host1", true, "maintenance")
	if v := e.view(id); slices.Contains(v.AllowedActions, "start") {
		t.Errorf("start is offered on a cordoned host: %v", v.AllowedActions)
	}
	if _, err := e.f.Act(ctx, id, "start", ActionOptions{}); !errors.As(err, &fe) || fe.Code != "HOST_CORDONED" {
		t.Errorf("start on a cordoned host = %v", err)
	}
	e.f.Cordon(ctx, "host1", false, "")
	if res, err := e.f.Act(ctx, id, "start", ActionOptions{}); err != nil || res.Worker.Lifecycle != "ACTIVE" {
		t.Errorf("start after uncordon = %+v, %v", res.Worker, err)
	}
}

func TestAFailedActionIsUndone(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.createActive()
	e.client.setErr("stop "+id, &controllerclient.APIError{Status: 502, Code: "DOCKER", Message: "daemon broke"})

	_, err := e.f.Act(ctx, id, "stop", ActionOptions{})
	var fe *Error
	if !errors.As(err, &fe) || fe.Status != 502 {
		t.Errorf("err = %v, want a 502", err)
	}
	if v := e.view(id); v.Lifecycle != "ACTIVE" {
		t.Errorf("lifecycle after the failed stop = %s, want ACTIVE", v.Lifecycle)
	}
	last := e.events(id)
	found := false
	for _, ev := range last {
		found = found || (ev.Kind == "ACTION" && strings.HasPrefix(ev.Message, "failed:"))
	}
	if !found {
		t.Error("the failure is not in the events")
	}
}

func TestKillAndRemoveWithVolumesLeft(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.createActive()
	if res, err := e.f.Act(ctx, id, "kill", ActionOptions{}); err != nil || res.Worker.Lifecycle != "STOPPING" {
		t.Fatalf("kill = %+v, %v", res.Worker, err)
	}
	e.poll(5 * time.Second)
	if v := e.view(id); v.Lifecycle != "STOPPED" || v.LastExitCode == nil || *v.LastExitCode != 137 {
		t.Errorf("after kill: %s %v", v.Lifecycle, v.LastExitCode)
	}

	e.client.removed = controllerapi.RemoveResponse{VolumesNotRemoved: []controllerapi.VolumeError{{Name: "automata-wl-" + id, Error: "in use"}}}
	res, err := e.f.Act(ctx, id, "remove", ActionOptions{RemoveVolumes: true})
	if err != nil || res.Worker.Lifecycle != "REMOVED" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "automata-wl-"+id) {
		t.Fatalf("remove = %+v, %v", res, err)
	}
	row, _ := e.store.Worker(ctx, id)
	if row.Lifecycle != "REMOVED" || !strings.HasPrefix(row.LifecycleReason, "VOLUMES_NOT_REMOVED: automata-wl-") {
		t.Errorf("stored = %s %s", row.Lifecycle, row.LifecycleReason)
	}
}

func TestRemoveOfAContainerAlreadyGone(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.createActive()
	e.f.Act(ctx, id, "kill", ActionOptions{})
	e.poll(5 * time.Second)
	e.client.setErr("remove "+id, &controllerclient.APIError{Status: 404, Code: "NOT_FOUND", Message: "no worker"})
	if res, err := e.f.Act(ctx, id, "remove", ActionOptions{}); err != nil || res.Worker.Lifecycle != "REMOVED" {
		t.Errorf("remove = %+v, %v", res.Worker, err)
	}
}

func TestAControllerThatStopsAnswering(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()
	e.client.setErr("snapshot", errBoom)

	// failed polls 5 and 10 s after the last snapshot keep what was known
	e.poll(5 * time.Second)
	e.poll(5 * time.Second)
	if v := e.view(id); v.Health != "HEALTHY" {
		t.Errorf("10 s after the last snapshot = %s, want HEALTHY kept", v.Health)
	}
	// 15 s after the last snapshot: counted from it, not from the first failed poll (which would take 20 s)
	e.poll(5 * time.Second)
	if v := e.view(id); v.Health != "UNKNOWN" || !slices.Equal(v.HealthReasons, []string{"WORKER_MACHINE_CONTROLLER_UNREACHABLE"}) {
		t.Errorf("after 15 s = %s %v", v.Health, v.HealthReasons)
	}
	if h, _ := e.f.Host("host1"); h.Reachable {
		t.Error("the host is still reachable")
	}

	e.client.setErr("snapshot", nil)
	e.client.set(id, ready(e.later(5*time.Second)))
	e.poll(5 * time.Second)
	if v := e.view(id); v.Health != "HEALTHY" {
		t.Errorf("back = %s", v.Health)
	}
}

func TestARestartOfTheControlPlaneKeepsThePicture(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()
	again := e.fleet(e.store)
	v, ok := again.Worker(id)
	if !ok || v.Lifecycle != "ACTIVE" || v.Health != "HEALTHY" {
		t.Fatalf("after a restart, before a poll: %+v", v)
	}
	e.f = again
	e.client.set(id, ready(e.later(5*time.Second)))
	e.poll(5 * time.Second)
	if v := e.view(id); v.Health != "HEALTHY" || v.Resources == nil {
		t.Errorf("after one poll: %+v", v)
	}
	// the restart counter starts again from what is seen: no false restart event
	if slices.Contains(e.kinds(id), "CONTAINER_RESTARTED") {
		t.Error("a restart was recorded after the control plane restarted")
	}
}

func TestViewsAndOverview(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.createActive()

	hosts := e.f.Hosts()
	if len(hosts) != 1 || !hosts[0].Reachable || hosts[0].Workers.Total != 1 || hosts[0].Workers.Responsive != 1 ||
		hosts[0].Capacity.CPUsReserved != 2 || hosts[0].Capacity.CPUs != 8 || hosts[0].MaxWorkers != 3 {
		t.Errorf("hosts = %+v", hosts)
	}
	ov := e.f.Overview()
	if ov.Workers != 1 || ov.ByHealth["HEALTHY"] != 1 || ov.ByLifecycle["ACTIVE"] != 1 || ov.Idle != 1 || len(ov.Hosts) != 1 {
		t.Errorf("overview = %+v", ov)
	}
	v := e.view(id)
	for _, tt := range []struct {
		f    Filter
		want bool
	}{
		{Filter{}, true}, {Filter{Host: "host1"}, true}, {Filter{Host: "host2"}, false},
		{Filter{Health: "HEALTHY"}, true}, {Filter{Health: "DOWN"}, false},
		{Filter{Lifecycle: "ACTIVE", Phase: "IDLE"}, true}, {Filter{Routine: "fuzz"}, false},
		{Filter{Q: strings.ToUpper(id[len(id)-3:])}, true}, {Filter{Q: "nope"}, false},
	} {
		if got := tt.f.Match(v); got != tt.want {
			t.Errorf("%+v.Match = %v, want %v", tt.f, got, tt.want)
		}
	}
	if e.f.LastPollAt() == nil {
		t.Error("no last poll time")
	}

	// a capacity warning when the new workers would go over the host
	big := e.tmpl
	big.Name, big.CPUs = "big", 4
	bigT, _ := e.store.CreateTemplate(ctx, big, t0)
	res, err := e.f.Create(ctx, "host1", bigT.ID, 2)
	if err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "10.0 / 8 CPUs") {
		t.Errorf("create = %+v, %v", res, err)
	}
}

func TestTimeseries(t *testing.T) {
	e := newEnv(t)
	id := e.createActive()
	for range 3 {
		e.client.set(id, ready(e.later(5*time.Second)))
		e.poll(5 * time.Second)
	}
	ts, err := e.f.WorkerTimeseries(context.Background(), id, []string{"cpu", "memory", "io", "health", "pids"}, "15m")
	if err != nil || ts.Source != "buffer" || ts.BucketSeconds != 5 {
		t.Fatalf("timeseries = %+v, %v", ts, err)
	}
	names := []string{}
	for _, s := range ts.Series {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"cpu", "cpuLimit", "memory", "memoryLimit", "blockRead", "blockWrite", "health", "pids"}) {
		t.Errorf("series = %v", names)
	}
	if len(ts.Series[0].Points) != 4 || ts.Series[0].Points[0][1] != 0.5 || ts.Series[6].Points[0][1] != "HEALTHY" {
		t.Errorf("cpu = %v health = %v", ts.Series[0].Points, ts.Series[6].Points)
	}

	hts, err := e.f.HostTimeseries(context.Background(), "host1", []string{"cpu", "memory", "load", "disk", "net"}, "1h")
	if err != nil || len(hts.Series) != 7 || hts.Series[4].Name != "disk:/host/var/lib/docker" || hts.Series[4].Points[0][1] != 0.6 {
		t.Errorf("host timeseries = %+v, %v", hts, err)
	}

	var fe *Error
	for _, call := range []func() error{
		func() error {
			_, err := e.f.WorkerTimeseries(context.Background(), id, []string{"cpu"}, "30d")
			return err
		},
		func() error {
			_, err := e.f.WorkerTimeseries(context.Background(), id, []string{"temperature"}, "15m")
			return err
		},
		func() error {
			_, err := e.f.WorkerTimeseries(context.Background(), "w-nope", []string{"cpu"}, "15m")
			return err
		},
		func() error {
			_, err := e.f.HostTimeseries(context.Background(), "host9", []string{"cpu"}, "15m")
			return err
		},
	} {
		if err := call(); !errors.As(err, &fe) {
			t.Errorf("err = %v, want an API error", err)
		}
	}
}

func TestValidateTemplate(t *testing.T) {
	good := store.DefaultTemplate()
	if err := ValidateTemplate(good); err != nil {
		t.Fatalf("the default template: %v", err)
	}
	tests := map[string]func(*store.Template){
		"no tag":         func(t *store.Template) { t.Image = "automata-worker" },
		"latest":         func(t *store.Template) { t.Image = "automata-worker:latest" },
		"registry port":  func(t *store.Template) { t.Image = "registry:5000/automata-worker" },
		"bad name":       func(t *store.Template) { t.Name = "Default" },
		"stop timeout":   func(t *store.Template) { t.StopTimeoutSeconds = 10 },
		"injected env":   func(t *store.Template) { t.Env = map[string]string{"WORKER_ID": "x"} },
		"drain negative": func(t *store.Template) { t.DrainTimeoutSeconds = -1 },
		"automata label": func(t *store.Template) { t.ExtraLabels = map[string]string{"automata.x": "y"} },
		"long text":      func(t *store.Template) { t.Description = strings.Repeat("x", 501) },
	}
	for name, change := range tests {
		tmpl := good
		change(&tmpl)
		if err := ValidateTemplate(tmpl); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for image, want := range map[string]bool{"registry:5000/automata-worker:0.4.0": true, "automata-worker@sha256:abc": true,
		"automata-worker:0.4.0": true, "automata-worker:latest": false, "automata-worker": false} {
		if hasTag(image) != want {
			t.Errorf("hasTag(%s) != %v", image, want)
		}
	}
}

func TestOutcomeOf(t *testing.T) {
	if got := outcomeOf(map[string]int64{"FINISHED": 1}, map[string]int64{"FINISHED": 1, "FAILED": 1}); got != "FAILED" {
		t.Errorf("outcome = %s", got)
	}
	if got := outcomeOf(map[string]int64{"FINISHED": 3}, map[string]int64{}); got != "UNKNOWN" {
		t.Errorf("outcome after a worker restart = %s", got)
	}
}

func TestPurge(t *testing.T) {
	e := newEnv(t)
	e.store.AddEvent(context.Background(), store.Event{WorkerID: "w-old", At: t0.AddDate(0, 0, -60), Kind: "ADOPTED"})
	e.f.Purge(context.Background())
	if evs, _ := e.store.Events(context.Background(), "w-old", 0, 10); len(evs) != 0 {
		t.Errorf("events = %v, want the old one purged", evs)
	}
}
