package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Kinds of worker events.
const (
	EventHealthChanged      = "HEALTH_CHANGED"
	EventLifecycleChanged   = "LIFECYCLE_CHANGED"
	EventAction             = "ACTION"
	EventContainerRestarted = "CONTAINER_RESTARTED"
	EventOOMKilled          = "OOM_KILLED"
	EventJobStarted         = "JOB_STARTED"
	EventJobEnded           = "JOB_ENDED"
	EventAdopted            = "ADOPTED"
	EventDisappeared        = "DISAPPEARED"
	EventProvisionFailed    = "PROVISION_FAILED"
	EventDrainTimeout       = "DRAIN_TIMEOUT"
)

// Event is a line of a worker's timeline. There is no actor: the control plane has no login.
type Event struct {
	ID       int64     `json:"id"`
	WorkerID string    `json:"workerId"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Reasons  []string  `json:"reasons"`
	Message  string    `json:"message"`
}

// AddEvent stores an event and returns it with its id.
func (s *Store) AddEvent(ctx context.Context, e Event) (Event, error) {
	if e.Reasons == nil {
		e.Reasons = []string{}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO worker_events (worker_id, at, kind, from_value, to_value, reasons, message)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, e.WorkerID, timeText(e.At), e.Kind, e.From, e.To, jsonOf(e.Reasons, "[]"), e.Message)
	if err != nil {
		return Event{}, fmt.Errorf("failed to store an event of %s: %w", e.WorkerID, err)
	}
	e.ID, _ = res.LastInsertId()
	return e, nil
}

// Events returns the events of a worker, newest first, with an id below before (0 for the newest).
func (s *Store) Events(ctx context.Context, workerID string, before int64, limit int) ([]Event, error) {
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, worker_id, at, kind, from_value, to_value, reasons, message
		FROM worker_events WHERE worker_id = ? AND id < ? ORDER BY id DESC LIMIT ?`, workerID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at, reasons string
		if err := rows.Scan(&e.ID, &e.WorkerID, &at, &e.Kind, &e.From, &e.To, &reasons, &e.Message); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		if err := json.Unmarshal([]byte(reasons), &e.Reasons); err != nil {
			return nil, fmt.Errorf("the reasons of event %d are broken: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountEvents counts the events of a kind of a worker since a time (e.g. the restarts of the last 15 minutes).
func (s *Store) CountEvents(ctx context.Context, workerID, kind string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM worker_events WHERE worker_id = ? AND kind = ? AND at >= ?`,
		workerID, kind, timeText(since)).Scan(&n)
	return n, err
}
