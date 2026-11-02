// Package store keeps the registry of the workers control plane in SQLite (plan/07-workers-control-plane.md §2):
// the state of the hosts, the templates, the workers and their events. Docker labels stay the truth of which
// containers exist; the store holds what the operator asked for and what was seen.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned for a row that does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned for a row that would break a unique key (e.g. a template name used twice).
var ErrConflict = errors.New("conflict")

type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path and applies the migrations it lacks.
func Open(path string) (*Store, error) {
	// auto_vacuum comes first: it only takes effect before the database file has its header, which WAL writes. On an
	// existing database it changes nothing. Incremental, so that purged history gives the disk space back.
	dsn := "file:" + path + "?_pragma=auto_vacuum(INCREMENTAL)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open the database %s: %w", path, err)
	}
	// SQLite has one writer; one connection makes that explicit and keeps the writes in order
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// migrate applies the embedded migrations in order, each in a transaction.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("failed to create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(name, "migrations/"), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s has no number: %w", name, err)
		}
		var applied int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s failed: %w", name, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, timeText(time.Now()))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// tx runs fn in a transaction, committed when fn returns nil.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin a transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}
	return nil
}

// Ping checks that the database answers (GET /healthz).
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// SizeBytes is the size of the database file (page_count × page_size).
func (s *Store) SizeBytes(ctx context.Context) (int64, error) {
	var pages, size int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size); err != nil {
		return 0, err
	}
	return pages * size, nil
}

// Purge deletes the events and the removed workers older than their retention, then gives free pages back.
func (s *Store) Purge(ctx context.Context, now time.Time, eventsDays, removedWorkersDays int) (events, workers int64, err error) {
	eventsBefore := timeText(now.AddDate(0, 0, -eventsDays))
	workersBefore := timeText(now.AddDate(0, 0, -removedWorkersDays))
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM worker_events WHERE at < ?`, eventsBefore)
		if err != nil {
			return err
		}
		events, _ = res.RowsAffected()
		res, err = tx.ExecContext(ctx, `DELETE FROM workers WHERE removed_at IS NOT NULL AND removed_at < ?`, workersBefore)
		if err != nil {
			return err
		}
		workers, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to purge: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum(2000)`); err != nil {
		return events, workers, fmt.Errorf("failed to vacuum: %w", err)
	}
	return events, workers, nil
}

// timeText writes a time the way the store keeps it: RFC 3339 UTC, sortable as text.
func timeText(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timeText(*t)
}

func timePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTime(ns.String)
	return &t
}

// isUnique tells whether err is a broken unique key.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
