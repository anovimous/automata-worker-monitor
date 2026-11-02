package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Template is how workers are created: image, env file, limits, network, cache size, drain behaviour.
type Template struct {
	ID                        int64             `json:"id"`
	Name                      string            `json:"name"`
	Description               string            `json:"description"`
	Image                     string            `json:"image"`
	PullPolicy                string            `json:"pullPolicy"`
	EnvFile                   string            `json:"envFile"`
	Env                       map[string]string `json:"env"`
	Network                   string            `json:"network"`
	ExtraHosts                []string          `json:"extraHosts"`
	CPUs                      float64           `json:"cpus"`
	MemoryBytes               int64             `json:"memoryBytes"`
	PidsLimit                 int64             `json:"pidsLimit"`
	WordlistCacheMaxBytes     int64             `json:"wordlistCacheMaxBytes"`
	TmpfsSocketsBytes         int64             `json:"tmpfsSocketsBytes"`
	RestartMaxRetries         int               `json:"restartMaxRetries"`
	StopTimeoutSeconds        int               `json:"stopTimeoutSeconds"`
	DrainTimeoutSeconds       int               `json:"drainTimeoutSeconds"`
	EscalateAfterDrainTimeout bool              `json:"escalateAfterDrainTimeout"`
	RemoveAfterDrain          bool              `json:"removeAfterDrain"`
	ExtraLabels               map[string]string `json:"extraLabels"`
	CreatedAt                 time.Time         `json:"createdAt"`
	UpdatedAt                 time.Time         `json:"updatedAt"`
}

const templateColumns = `id, name, description, image, pull_policy, env_file, env_json, network, extra_hosts_json, cpus,
	memory_bytes, pids_limit, wordlist_cache_max_bytes, tmpfs_sockets_bytes, restart_max_retries, stop_timeout_seconds,
	drain_timeout_seconds, escalate_after_drain_timeout, remove_after_drain, extra_labels_json, created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanTemplate(row scanner) (Template, error) {
	var t Template
	var env, hosts, labels, created, updated string
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Image, &t.PullPolicy, &t.EnvFile, &env, &t.Network, &hosts, &t.CPUs,
		&t.MemoryBytes, &t.PidsLimit, &t.WordlistCacheMaxBytes, &t.TmpfsSocketsBytes, &t.RestartMaxRetries, &t.StopTimeoutSeconds,
		&t.DrainTimeoutSeconds, &t.EscalateAfterDrainTimeout, &t.RemoveAfterDrain, &labels, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	if err != nil {
		return Template{}, err
	}
	if err := unmarshalAll([]string{env, hosts, labels}, &t.Env, &t.ExtraHosts, &t.ExtraLabels); err != nil {
		return Template{}, err
	}
	t.CreatedAt, t.UpdatedAt = parseTime(created), parseTime(updated)
	return t, nil
}

func unmarshalAll(docs []string, into ...any) error {
	for i, doc := range docs {
		if err := json.Unmarshal([]byte(doc), into[i]); err != nil {
			return fmt.Errorf("a stored JSON column is broken: %w", err)
		}
	}
	return nil
}

// jsonOf writes v, with an empty map or list for nil so that the column keeps its shape.
func jsonOf(v any, empty string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return empty
	}
	return string(b)
}

func templateArgs(t Template) []any {
	return []any{t.Name, t.Description, t.Image, t.PullPolicy, t.EnvFile, jsonOf(t.Env, "{}"), t.Network,
		jsonOf(t.ExtraHosts, "[]"), t.CPUs, t.MemoryBytes, t.PidsLimit, t.WordlistCacheMaxBytes, t.TmpfsSocketsBytes,
		t.RestartMaxRetries, t.StopTimeoutSeconds, t.DrainTimeoutSeconds, t.EscalateAfterDrainTimeout, t.RemoveAfterDrain,
		jsonOf(t.ExtraLabels, "{}")}
}

// CreateTemplate stores a new template and returns it with its id; a name in use is ErrConflict.
func (s *Store) CreateTemplate(ctx context.Context, t Template, now time.Time) (Template, error) {
	args := append(templateArgs(t), timeText(now), timeText(now))
	res, err := s.db.ExecContext(ctx, `INSERT INTO templates (name, description, image, pull_policy, env_file, env_json,
		network, extra_hosts_json, cpus, memory_bytes, pids_limit, wordlist_cache_max_bytes, tmpfs_sockets_bytes,
		restart_max_retries, stop_timeout_seconds, drain_timeout_seconds, escalate_after_drain_timeout, remove_after_drain,
		extra_labels_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	if isUnique(err) {
		return Template{}, fmt.Errorf("the template name %s is in use: %w", t.Name, ErrConflict)
	}
	if err != nil {
		return Template{}, fmt.Errorf("failed to store the template: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.Template(ctx, id)
}

// UpdateTemplate replaces a template; the workers created from it keep their own copy.
func (s *Store) UpdateTemplate(ctx context.Context, t Template, now time.Time) (Template, error) {
	args := append(templateArgs(t), timeText(now), t.ID)
	res, err := s.db.ExecContext(ctx, `UPDATE templates SET name = ?, description = ?, image = ?, pull_policy = ?,
		env_file = ?, env_json = ?, network = ?, extra_hosts_json = ?, cpus = ?, memory_bytes = ?, pids_limit = ?,
		wordlist_cache_max_bytes = ?, tmpfs_sockets_bytes = ?, restart_max_retries = ?, stop_timeout_seconds = ?,
		drain_timeout_seconds = ?, escalate_after_drain_timeout = ?, remove_after_drain = ?, extra_labels_json = ?,
		updated_at = ? WHERE id = ?`, args...)
	if isUnique(err) {
		return Template{}, fmt.Errorf("the template name %s is in use: %w", t.Name, ErrConflict)
	}
	if err != nil {
		return Template{}, fmt.Errorf("failed to update the template: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Template{}, ErrNotFound
	}
	return s.Template(ctx, t.ID)
}

// DeleteTemplate deletes a template; it is allowed while workers use it (each keeps its template_snapshot).
func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete the template: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Template(ctx context.Context, id int64) (Template, error) {
	return scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateColumns+` FROM templates WHERE id = ?`, id))
}

func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateColumns+` FROM templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DefaultTemplate is the template seeded on the first start.
func DefaultTemplate() Template {
	return Template{
		Name: "default", Description: "Seeded on the first start; edit it to your image and network.",
		Image: "automata-worker:0.4.0", PullPolicy: "if-missing", EnvFile: "default", Env: map[string]string{},
		Network: "automata_backend", ExtraHosts: []string{}, CPUs: 2, MemoryBytes: 4 << 30, PidsLimit: 512,
		WordlistCacheMaxBytes: 1610612736, TmpfsSocketsBytes: 16 << 20, RestartMaxRetries: 5, StopTimeoutSeconds: 60,
		ExtraLabels: map[string]string{},
	}
}

// SeedDefault inserts the default template when there is no template at all.
func (s *Store) SeedDefault(ctx context.Context, now time.Time) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM templates`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.CreateTemplate(ctx, DefaultTemplate(), now)
	return err
}
