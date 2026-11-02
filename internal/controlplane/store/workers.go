package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Worker is the stored row of a worker: what the operator asked for (lifecycle) and what was seen last (health).
type Worker struct {
	ID               string
	Host             string
	TemplateID       *int64
	TemplateSnapshot json.RawMessage // the template as used at the creation
	Managed          bool
	ContainerID      string
	Lifecycle        string
	LifecycleReason  string
	LifecycleSince   time.Time
	DrainDeadline    *time.Time
	Health           string
	HealthReasons    []string
	HealthSince      time.Time
	LastSeenAt       *time.Time
	LastStatus       json.RawMessage // null when none was ever read
	Version          string
	CreatedAt        time.Time
	RemovedAt        *time.Time
}

const workerColumns = `id, host, template_id, template_snapshot, managed, container_id, lifecycle, lifecycle_reason,
	lifecycle_since, drain_deadline, health, health_reasons, health_since, last_seen_at, last_status_json, version,
	created_at, removed_at`

func scanWorker(row scanner) (Worker, error) {
	var w Worker
	var templateID sql.NullInt64
	var snapshot, reasons, lifecycleSince, healthSince, created string
	var deadline, seen, status, removed sql.NullString
	err := row.Scan(&w.ID, &w.Host, &templateID, &snapshot, &w.Managed, &w.ContainerID, &w.Lifecycle, &w.LifecycleReason,
		&lifecycleSince, &deadline, &w.Health, &reasons, &healthSince, &seen, &status, &w.Version, &created, &removed)
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	if templateID.Valid {
		id := templateID.Int64
		w.TemplateID = &id
	}
	w.TemplateSnapshot = json.RawMessage(snapshot)
	if err := json.Unmarshal([]byte(reasons), &w.HealthReasons); err != nil {
		return Worker{}, fmt.Errorf("the health reasons of %s are broken: %w", w.ID, err)
	}
	if status.Valid {
		w.LastStatus = json.RawMessage(status.String)
	}
	w.LifecycleSince, w.HealthSince, w.CreatedAt = parseTime(lifecycleSince), parseTime(healthSince), parseTime(created)
	w.DrainDeadline, w.LastSeenAt, w.RemovedAt = timePtr(deadline), timePtr(seen), timePtr(removed)
	return w, nil
}

func workerArgs(w Worker) []any {
	var status any
	if len(w.LastStatus) > 0 {
		status = string(w.LastStatus)
	}
	snapshot := string(w.TemplateSnapshot)
	if snapshot == "" {
		snapshot = "{}"
	}
	return []any{w.Host, w.TemplateID, snapshot, w.Managed, w.ContainerID, w.Lifecycle, w.LifecycleReason,
		timeText(w.LifecycleSince), nullTime(w.DrainDeadline), w.Health, jsonOf(w.HealthReasons, "[]"),
		timeText(w.HealthSince), nullTime(w.LastSeenAt), status, w.Version, timeText(w.CreatedAt), nullTime(w.RemovedAt)}
}

// InsertWorker stores a new worker; an id in use is ErrConflict.
func (s *Store) InsertWorker(ctx context.Context, w Worker) error {
	args := append([]any{w.ID}, workerArgs(w)...)
	_, err := s.db.ExecContext(ctx, `INSERT INTO workers (`+workerColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	if isUnique(err) {
		return fmt.Errorf("the worker %s exists: %w", w.ID, ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("failed to store the worker %s: %w", w.ID, err)
	}
	return nil
}

// UpdateWorker writes every column of a worker.
func (s *Store) UpdateWorker(ctx context.Context, w Worker) error {
	args := append(workerArgs(w), w.ID)
	res, err := s.db.ExecContext(ctx, `UPDATE workers SET host = ?, template_id = ?, template_snapshot = ?, managed = ?,
		container_id = ?, lifecycle = ?, lifecycle_reason = ?, lifecycle_since = ?, drain_deadline = ?, health = ?,
		health_reasons = ?, health_since = ?, last_seen_at = ?, last_status_json = ?, version = ?, created_at = ?,
		removed_at = ? WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("failed to update the worker %s: %w", w.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Worker(ctx context.Context, id string) (Worker, error) {
	return scanWorker(s.db.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM workers WHERE id = ?`, id))
}

// Workers returns the workers, the removed ones too when asked, ordered by id.
func (s *Store) Workers(ctx context.Context, withRemoved bool) ([]Worker, error) {
	q := `SELECT ` + workerColumns + ` FROM workers`
	if !withRemoved {
		q += ` WHERE removed_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Worker{}
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
