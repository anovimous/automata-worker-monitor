package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HostState is what the control plane keeps of a configured host.
type HostState struct {
	Name           string
	Cordoned       bool
	CordonedReason string
	LastSeenAt     *time.Time
}

// HostState returns the state of a host; a host never seen has the zero state.
func (s *Store) HostState(ctx context.Context, name string) (HostState, error) {
	h := HostState{Name: name}
	var seen sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT cordoned, cordoned_reason, last_seen_at FROM host_state WHERE name = ?`, name).
		Scan(&h.Cordoned, &h.CordonedReason, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return h, nil
	}
	if err != nil {
		return HostState{}, err
	}
	h.LastSeenAt = timePtr(seen)
	return h, nil
}

// SetCordon cordons (no new workers) or uncordons a host.
func (s *Store) SetCordon(ctx context.Context, name string, cordoned bool, reason string) error {
	if !cordoned {
		reason = ""
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_state (name, cordoned, cordoned_reason) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET cordoned = excluded.cordoned, cordoned_reason = excluded.cordoned_reason`,
		name, cordoned, reason)
	return err
}

// SetHostSeen records the time of the last successful snapshot of a host.
func (s *Store) SetHostSeen(ctx context.Context, name string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_state (name, last_seen_at) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET last_seen_at = excluded.last_seen_at`, name, timeText(at))
	return err
}
