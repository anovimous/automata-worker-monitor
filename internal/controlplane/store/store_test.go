package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monitor.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestOpenMigratesAnEmptyDatabaseOnce(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()

	var vacuum int
	s.db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&vacuum)
	if vacuum != 2 {
		t.Errorf("auto_vacuum = %d, want 2 (incremental)", vacuum)
	}
	var journal string
	s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal)
	if journal != "wal" {
		t.Errorf("journal_mode = %s, want wal", journal)
	}
	var fk int
	s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Error("foreign keys are off")
	}
	for _, table := range []string{"host_state", "templates", "workers", "worker_events"} {
		var n int
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if n != 1 {
			t.Errorf("table %s is missing", table)
		}
	}
	for _, table := range []string{"sessions", "audit_log"} {
		var n int
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n)
		if n != 0 {
			t.Errorf("table %s exists, there is no login and no audit", table)
		}
	}
	s.Close()

	// opening again applies nothing twice
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()
	var versions int
	again.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions)
	files, _ := migrations.ReadDir("migrations")
	if versions != len(files) {
		t.Errorf("migrations recorded = %d, want one per file (%d)", versions, len(files))
	}
	if err := again.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
	if size, err := again.SizeBytes(ctx); err != nil || size <= 0 {
		t.Errorf("SizeBytes = %d, %v", size, err)
	}
}

func TestOpenFailsOnABadPath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "dir", "monitor.db")); err == nil {
		t.Error("Open in a missing directory returned no error")
	}
}

func TestHostState(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	h, err := s.HostState(ctx, "host1")
	if err != nil || h.Cordoned || h.LastSeenAt != nil || h.Name != "host1" {
		t.Fatalf("a host never seen = %+v, %v", h, err)
	}
	s.SetHostSeen(ctx, "host1", t0)
	s.SetCordon(ctx, "host1", true, "disk full")
	h, _ = s.HostState(ctx, "host1")
	if !h.Cordoned || h.CordonedReason != "disk full" || h.LastSeenAt == nil || !h.LastSeenAt.Equal(t0) {
		t.Errorf("host = %+v", h)
	}
	s.SetCordon(ctx, "host1", false, "ignored")
	s.SetHostSeen(ctx, "host1", t0.Add(time.Minute))
	h, _ = s.HostState(ctx, "host1")
	if h.Cordoned || h.CordonedReason != "" || !h.LastSeenAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("after uncordon = %+v", h)
	}
}

func TestTemplates(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	created, err := s.CreateTemplate(ctx, DefaultTemplate(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Name != "default" || created.MemoryBytes != 4<<30 || !created.CreatedAt.Equal(t0) {
		t.Errorf("created = %+v", created)
	}
	if _, err := s.CreateTemplate(ctx, DefaultTemplate(), t0); !errors.Is(err, ErrConflict) {
		t.Errorf("a second default = %v, want ErrConflict", err)
	}

	created.Env = map[string]string{"CENTRAL_POLL_INTERVAL_SECONDS": "5"}
	created.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	created.ExtraLabels = map[string]string{"team": "red"}
	created.RemoveAfterDrain, created.DrainTimeoutSeconds = true, 3600
	updated, err := s.UpdateTemplate(ctx, created, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Env["CENTRAL_POLL_INTERVAL_SECONDS"] != "5" || updated.ExtraHosts[0] != "host.docker.internal:host-gateway" ||
		updated.ExtraLabels["team"] != "red" || !updated.RemoveAfterDrain || updated.DrainTimeoutSeconds != 3600 ||
		!updated.UpdatedAt.Equal(t0.Add(time.Hour)) || !updated.CreatedAt.Equal(t0) {
		t.Errorf("updated = %+v", updated)
	}

	other := DefaultTemplate()
	other.Name = "small"
	other.MemoryBytes = 1 << 30
	small, _ := s.CreateTemplate(ctx, other, t0)
	small.Name = "default"
	if _, err := s.UpdateTemplate(ctx, small, t0); !errors.Is(err, ErrConflict) {
		t.Errorf("renaming to a name in use = %v, want ErrConflict", err)
	}

	list, _ := s.Templates(ctx)
	if len(list) != 2 || list[0].Name != "default" || list[1].Name != "small" {
		t.Errorf("templates = %+v", list)
	}
	if err := s.DeleteTemplate(ctx, small.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Template(ctx, small.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted template = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTemplate(ctx, small.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
	missing := DefaultTemplate()
	missing.ID = 999
	if _, err := s.UpdateTemplate(ctx, missing, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing template = %v, want ErrNotFound", err)
	}
}

func TestTheCheckConstraintsOfATemplate(t *testing.T) {
	s, _ := openTemp(t)
	bad := DefaultTemplate()
	bad.StopTimeoutSeconds = 10
	if _, err := s.CreateTemplate(context.Background(), bad, t0); err == nil {
		t.Error("a stop timeout of 10 s was stored")
	}
}

func TestSeedDefaultOnlyOnAnEmptyTable(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.SeedDefault(ctx, t0)
	s.SeedDefault(ctx, t0)
	if list, _ := s.Templates(ctx); len(list) != 1 {
		t.Errorf("templates after two seeds = %d, want 1", len(list))
	}
}

func TestSeedDefaultUsesTheCurrentWorkerImage(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.SeedDefault(ctx, t0); err != nil {
		t.Fatal(err)
	}
	list, err := s.Templates(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("templates = %d, err %v, want 1", len(list), err)
	}
	if list[0].Name != "default" || list[0].Image != "automata-worker:0.10.0" {
		t.Errorf("seeded template = %q with image %q, want default with automata-worker:0.10.0", list[0].Name, list[0].Image)
	}
}

func worker(id string) Worker {
	return Worker{ID: id, Host: "host1", Managed: true, Lifecycle: "PROVISIONING", LifecycleSince: t0, Health: "UNKNOWN",
		HealthReasons: []string{}, HealthSince: t0, CreatedAt: t0, TemplateSnapshot: json.RawMessage(`{"name":"default"}`)}
}

func TestWorkers(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	tmpl, _ := s.CreateTemplate(ctx, DefaultTemplate(), t0)

	w := worker("w-host1-aaaaaa")
	w.TemplateID = &tmpl.ID
	if err := s.InsertWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertWorker(ctx, w); !errors.Is(err, ErrConflict) {
		t.Errorf("a second insert = %v, want ErrConflict", err)
	}

	deadline, seen := t0.Add(time.Hour), t0.Add(time.Minute)
	w.ContainerID, w.Lifecycle, w.LifecycleReason, w.DrainDeadline = "c1", "DRAINING", "", &deadline
	w.Health, w.HealthReasons, w.LastSeenAt, w.Version = "DEGRADED", []string{"CORE_ERRORS"}, &seen, "0.4.0"
	w.LastStatus = json.RawMessage(`{"schemaVersion":1}`)
	if err := s.UpdateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := s.Worker(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID != "c1" || got.Lifecycle != "DRAINING" || !got.DrainDeadline.Equal(deadline) || got.Health != "DEGRADED" ||
		got.HealthReasons[0] != "CORE_ERRORS" || !got.LastSeenAt.Equal(seen) || got.Version != "0.4.0" ||
		string(got.LastStatus) != `{"schemaVersion":1}` || *got.TemplateID != tmpl.ID || string(got.TemplateSnapshot) != `{"name":"default"}` {
		t.Errorf("worker = %+v", got)
	}

	// deleting the template keeps the worker and its copy of it
	s.DeleteTemplate(ctx, tmpl.ID)
	got, _ = s.Worker(ctx, w.ID)
	if got.TemplateID != nil || string(got.TemplateSnapshot) != `{"name":"default"}` {
		t.Errorf("after the template was deleted: id %v, snapshot %s", got.TemplateID, got.TemplateSnapshot)
	}

	removed := worker("w-host1-bbbbbb")
	at := t0.Add(2 * time.Hour)
	removed.RemovedAt = &at
	s.InsertWorker(ctx, removed)
	if list, _ := s.Workers(ctx, false); len(list) != 1 || list[0].ID != w.ID {
		t.Errorf("workers = %+v, want the removed one left out", list)
	}
	if list, _ := s.Workers(ctx, true); len(list) != 2 {
		t.Errorf("workers with the removed = %d, want 2", len(list))
	}
	if _, err := s.Worker(ctx, "w-nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing worker = %v", err)
	}
	if err := s.UpdateWorker(ctx, worker("w-nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing worker = %v", err)
	}
}

func TestEvents(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for i := range 5 {
		_, err := s.AddEvent(ctx, Event{WorkerID: "w-1", At: t0.Add(time.Duration(i) * time.Minute), Kind: EventContainerRestarted})
		if err != nil {
			t.Fatal(err)
		}
	}
	e, _ := s.AddEvent(ctx, Event{WorkerID: "w-1", At: t0.Add(time.Hour), Kind: EventHealthChanged, From: "HEALTHY", To: "STUCK",
		Reasons: []string{"NO_PROGRESS"}, Message: "no progress for 1602 s"})
	s.AddEvent(ctx, Event{WorkerID: "w-2", At: t0, Kind: EventAdopted})

	page, _ := s.Events(ctx, "w-1", 0, 3)
	if len(page) != 3 || page[0].ID != e.ID || page[0].Reasons[0] != "NO_PROGRESS" || page[0].To != "STUCK" || !page[0].At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("first page = %+v", page)
	}
	next, _ := s.Events(ctx, "w-1", page[2].ID, 10)
	if len(next) != 3 || next[0].ID >= page[2].ID {
		t.Errorf("next page = %+v", next)
	}

	n, _ := s.CountEvents(ctx, "w-1", EventContainerRestarted, t0.Add(2*time.Minute))
	if n != 3 {
		t.Errorf("restarts since t0+2m = %d, want 3", n)
	}
}

func TestPurge(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := t0.AddDate(0, 0, 40)
	s.AddEvent(ctx, Event{WorkerID: "w-1", At: t0, Kind: EventAdopted})
	s.AddEvent(ctx, Event{WorkerID: "w-1", At: now.Add(-time.Hour), Kind: EventAdopted})
	old, recent := worker("w-old000-aaaaaa"), worker("w-new000-aaaaaa")
	oldAt, recentAt := t0, now.Add(-24*time.Hour)
	old.RemovedAt, recent.RemovedAt = &oldAt, &recentAt
	s.InsertWorker(ctx, old)
	s.InsertWorker(ctx, recent)
	s.InsertWorker(ctx, worker("w-live00-aaaaaa"))

	events, workers, err := s.Purge(ctx, now, 30, 30)
	if err != nil || events != 1 || workers != 1 {
		t.Errorf("Purge = %d events, %d workers, %v; want 1 and 1", events, workers, err)
	}
	if list, _ := s.Workers(ctx, true); len(list) != 2 {
		t.Errorf("workers left = %d, want 2", len(list))
	}
}

func TestTheDefaultTemplateUsesTheHostNetwork(t *testing.T) {
	// like the worker's own compose file: the env file reaches the core through its published ports
	if d := DefaultTemplate(); d.Network != "host" || d.EnvFile != "default" {
		t.Errorf("default template network %q env file %q, want host and default", d.Network, d.EnvFile)
	}
}
