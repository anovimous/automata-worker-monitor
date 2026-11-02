package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// WorkerSample is one minute of a worker (plan/09 §1.2).
type WorkerSample struct {
	WorkerID        string
	Minute          int64 // unix seconds, floored to 60
	Polls           int
	ResourcePolls   int // the polls that had the resources: the weight of their averages
	RatePolls       int // the polls that had the I/O rates
	CPUAvg, CPUMax  *float64
	CPULimit        *float64
	MemAvg, MemMax  *int64
	MemLimit        *int64
	PidsMax         *int64
	BlkReadBps      *float64
	BlkWriteBps     *float64
	HealthWorst     string
	ResponsivePolls int
	BusyPolls       int
	PhaseLast       string
	JobIDLast       *int64
	RoutineLast     *string
}

// HostSample is one minute of a host.
type HostSample struct {
	Host           string
	Minute         int64
	Polls          int
	ReachablePolls int // the polls that answered: the weight of the averages
	CPUAvg, CPUMax *float64
	MemUsedAvg     *int64
	MemTotal       *int64
	Load1Avg       *float64
	Disks          []DiskMax
	NetRxBps       *float64
	NetTxBps       *float64
}

type DiskMax struct {
	Path         string  `json:"path"`
	UsedRatioMax float64 `json:"usedRatioMax"`
}

// the order of "worst" for the health of a minute
var healthRank = map[string]int{"NONE": 0, "HEALTHY": 1, "STARTING": 2, "UNKNOWN": 3, "DEGRADED": 4, "STUCK": 5,
	"UNRESPONSIVE": 6, "DOWN": 7}

// WorstHealth is the worse of two healths.
func WorstHealth(a, b string) string {
	if healthRank[b] > healthRank[a] {
		return b
	}
	return a
}

// MergeWorkerSamples combines two parts of the same minute, b the later one: a restart of the control plane within a
// minute writes the minute again, and it must add to the row, not replace it.
func MergeWorkerSamples(a, b WorkerSample) WorkerSample {
	m := b
	m.Polls, m.ResourcePolls, m.RatePolls = a.Polls+b.Polls, a.ResourcePolls+b.ResourcePolls, a.RatePolls+b.RatePolls
	m.CPUAvg = weighted(a.CPUAvg, a.ResourcePolls, b.CPUAvg, b.ResourcePolls)
	m.CPUMax = maxOf(a.CPUMax, b.CPUMax)
	m.CPULimit = lastOf(a.CPULimit, b.CPULimit)
	m.MemAvg = weightedInt(a.MemAvg, a.ResourcePolls, b.MemAvg, b.ResourcePolls)
	m.MemMax = maxOf(a.MemMax, b.MemMax)
	m.MemLimit = lastOf(a.MemLimit, b.MemLimit)
	m.PidsMax = maxOf(a.PidsMax, b.PidsMax)
	m.BlkReadBps = weighted(a.BlkReadBps, a.RatePolls, b.BlkReadBps, b.RatePolls)
	m.BlkWriteBps = weighted(a.BlkWriteBps, a.RatePolls, b.BlkWriteBps, b.RatePolls)
	m.HealthWorst = WorstHealth(a.HealthWorst, b.HealthWorst)
	m.ResponsivePolls, m.BusyPolls = a.ResponsivePolls+b.ResponsivePolls, a.BusyPolls+b.BusyPolls
	if b.Polls == 0 {
		m.PhaseLast, m.JobIDLast, m.RoutineLast = a.PhaseLast, a.JobIDLast, a.RoutineLast
	}
	return m
}

// MergeHostSamples combines two parts of the same minute of a host, b the later one.
func MergeHostSamples(a, b HostSample) HostSample {
	m := b
	m.Polls, m.ReachablePolls = a.Polls+b.Polls, a.ReachablePolls+b.ReachablePolls
	m.CPUAvg = weighted(a.CPUAvg, a.ReachablePolls, b.CPUAvg, b.ReachablePolls)
	m.CPUMax = maxOf(a.CPUMax, b.CPUMax)
	m.MemUsedAvg = weightedInt(a.MemUsedAvg, a.ReachablePolls, b.MemUsedAvg, b.ReachablePolls)
	m.MemTotal = lastOf(a.MemTotal, b.MemTotal)
	m.Load1Avg = weighted(a.Load1Avg, a.ReachablePolls, b.Load1Avg, b.ReachablePolls)
	m.NetRxBps = weighted(a.NetRxBps, a.ReachablePolls, b.NetRxBps, b.ReachablePolls)
	m.NetTxBps = weighted(a.NetTxBps, a.ReachablePolls, b.NetTxBps, b.ReachablePolls)
	m.Disks = mergeDisks(a.Disks, b.Disks)
	return m
}

func mergeDisks(a, b []DiskMax) []DiskMax {
	out := append([]DiskMax{}, a...)
	for _, d := range b {
		found := false
		for i := range out {
			if out[i].Path == d.Path {
				out[i].UsedRatioMax = max(out[i].UsedRatioMax, d.UsedRatioMax)
				found = true
			}
		}
		if !found {
			out = append(out, d)
		}
	}
	return out
}

func weighted(a *float64, na int, b *float64, nb int) *float64 {
	switch {
	case a == nil || na == 0:
		return b
	case b == nil || nb == 0:
		return a
	}
	v := (*a*float64(na) + *b*float64(nb)) / float64(na+nb)
	return &v
}

func weightedInt(a *int64, na int, b *int64, nb int) *int64 {
	var fa, fb *float64
	if a != nil {
		v := float64(*a)
		fa = &v
	}
	if b != nil {
		v := float64(*b)
		fb = &v
	}
	w := weighted(fa, na, fb, nb)
	if w == nil {
		return nil
	}
	v := int64(*w)
	return &v
}

func maxOf[T int64 | float64](a, b *T) *T {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case *b > *a:
		return b
	}
	return a
}

func lastOf[T any](a, b *T) *T {
	if b != nil {
		return b
	}
	return a
}

const workerSampleColumns = `worker_id, minute, polls, resource_polls, rate_polls, cpu_cores_avg, cpu_cores_max, cpu_limit,
	mem_ws_avg, mem_ws_max, mem_limit, pids_max, blk_read_bps, blk_write_bps, health_worst, responsive_polls, busy_polls,
	phase_last, job_id_last, routine_key_last`

const hostSampleColumns = `host, minute, polls, reachable_polls, cpu_ratio_avg, cpu_ratio_max, mem_used_avg, mem_total,
	load1_avg, disk_json, net_rx_bps, net_tx_bps`

func scanWorkerSample(row scanner) (WorkerSample, error) {
	var s WorkerSample
	err := row.Scan(&s.WorkerID, &s.Minute, &s.Polls, &s.ResourcePolls, &s.RatePolls, &s.CPUAvg, &s.CPUMax, &s.CPULimit,
		&s.MemAvg, &s.MemMax, &s.MemLimit, &s.PidsMax, &s.BlkReadBps, &s.BlkWriteBps, &s.HealthWorst, &s.ResponsivePolls,
		&s.BusyPolls, &s.PhaseLast, &s.JobIDLast, &s.RoutineLast)
	return s, err
}

func scanHostSample(row scanner) (HostSample, error) {
	var s HostSample
	var disks string
	err := row.Scan(&s.Host, &s.Minute, &s.Polls, &s.ReachablePolls, &s.CPUAvg, &s.CPUMax, &s.MemUsedAvg, &s.MemTotal,
		&s.Load1Avg, &disks, &s.NetRxBps, &s.NetTxBps)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(disks), &s.Disks); err != nil {
		return s, fmt.Errorf("the disks of a host sample are broken: %w", err)
	}
	return s, nil
}

// WriteSamples writes the finished minute of every worker and host in one transaction, merged with a row of the same
// minute written before (a restart of the control plane within the minute).
func (s *Store) WriteSamples(ctx context.Context, workers []WorkerSample, hosts []HostSample) error {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, w := range workers {
			old, err := scanWorkerSample(tx.QueryRowContext(ctx, `SELECT `+workerSampleColumns+` FROM worker_samples
				WHERE worker_id = ? AND minute = ?`, w.WorkerID, w.Minute))
			if err == nil {
				w = MergeWorkerSamples(old, w)
			} else if err != sql.ErrNoRows {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO worker_samples (`+workerSampleColumns+`)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				w.WorkerID, w.Minute, w.Polls, w.ResourcePolls, w.RatePolls, w.CPUAvg, w.CPUMax, w.CPULimit, w.MemAvg,
				w.MemMax, w.MemLimit, w.PidsMax, w.BlkReadBps, w.BlkWriteBps, w.HealthWorst, w.ResponsivePolls, w.BusyPolls,
				w.PhaseLast, w.JobIDLast, w.RoutineLast); err != nil {
				return err
			}
		}
		for _, h := range hosts {
			old, err := scanHostSample(tx.QueryRowContext(ctx, `SELECT `+hostSampleColumns+` FROM host_samples
				WHERE host = ? AND minute = ?`, h.Host, h.Minute))
			if err == nil {
				h = MergeHostSamples(old, h)
			} else if err != sql.ErrNoRows {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO host_samples (`+hostSampleColumns+`)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				h.Host, h.Minute, h.Polls, h.ReachablePolls, h.CPUAvg, h.CPUMax, h.MemUsedAvg, h.MemTotal, h.Load1Avg,
				jsonOf(h.Disks, "[]"), h.NetRxBps, h.NetTxBps); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to write the samples: %w", err)
	}
	return nil
}

// WorkerSamples returns the minutes of a worker from from to to (unix seconds, both included), oldest first.
func (s *Store) WorkerSamples(ctx context.Context, workerID string, from, to int64) ([]WorkerSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workerSampleColumns+` FROM worker_samples
		WHERE worker_id = ? AND minute BETWEEN ? AND ? ORDER BY minute`, workerID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkerSample
	for rows.Next() {
		w, err := scanWorkerSample(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// HostSamples returns the minutes of a host from from to to, oldest first.
func (s *Store) HostSamples(ctx context.Context, host string, from, to int64) ([]HostSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hostSampleColumns+` FROM host_samples
		WHERE host = ? AND minute BETWEEN ? AND ? ORDER BY minute`, host, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostSample
	for rows.Next() {
		h, err := scanHostSample(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HasWorkerSamples tells whether a worker has any minute stored (a removed worker's history is kept).
func (s *Store) HasWorkerSamples(ctx context.Context, workerID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM worker_samples WHERE worker_id = ? LIMIT 1)`, workerID).Scan(&n)
	return n > 0, err
}

// Usage is how a worker was over a range: its polls, the responsive ones and the busy ones.
type Usage struct {
	Polls      int64
	Responsive int64
	Busy       int64
}

// Usages returns the usage of every worker with minutes since from, by worker id.
func (s *Store) Usages(ctx context.Context, from int64) (map[string]Usage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT worker_id, sum(polls), sum(responsive_polls), sum(busy_polls)
		FROM worker_samples WHERE minute >= ? GROUP BY worker_id`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Usage{}
	for rows.Next() {
		var id string
		var u Usage
		if err := rows.Scan(&id, &u.Polls, &u.Responsive, &u.Busy); err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

// FleetMinute is the fleet in one minute: how many workers had a row, and how much of the minute they were busy or
// responsive (in workers: 3 workers busy half of the minute is 1.5).
type FleetMinute struct {
	Minute     int64
	Active     int
	Busy       float64
	Responsive float64
}

// FleetMinutes returns the fleet minute by minute from from to to.
func (s *Store) FleetMinutes(ctx context.Context, from, to int64) ([]FleetMinute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT minute, count(*), sum(CAST(busy_polls AS REAL) / polls),
		sum(CAST(responsive_polls AS REAL) / polls) FROM worker_samples WHERE minute BETWEEN ? AND ? AND polls > 0
		GROUP BY minute ORDER BY minute`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetMinute
	for rows.Next() {
		var m FleetMinute
		if err := rows.Scan(&m.Minute, &m.Active, &m.Busy, &m.Responsive); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// JobEnd is a job that ended: when, and how.
type JobEnd struct {
	At      time.Time
	Outcome string
}

// JobsEnded returns the JOB_ENDED events from from to to, of one worker or ("" ) of all.
func (s *Store) JobsEnded(ctx context.Context, workerID string, from, to time.Time) ([]JobEnd, error) {
	q := `SELECT at, to_value FROM worker_events WHERE kind = ? AND at BETWEEN ? AND ?`
	args := []any{EventJobEnded, timeText(from), timeText(to)}
	if workerID != "" {
		q += ` AND worker_id = ?`
		args = append(args, workerID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobEnd
	for rows.Next() {
		var at string
		var j JobEnd
		if err := rows.Scan(&at, &j.Outcome); err != nil {
			return nil, err
		}
		j.At = parseTime(at)
		out = append(out, j)
	}
	return out, rows.Err()
}

// PurgeSamples deletes the minutes before before (unix seconds), then gives free pages back.
func (s *Store) PurgeSamples(ctx context.Context, before int64) (int64, error) {
	var n int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"worker_samples", "host_samples"} {
			res, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE minute < ?`, before)
			if err != nil {
				return err
			}
			deleted, _ := res.RowsAffected()
			n += deleted
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to purge the samples: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum(2000)`); err != nil {
		return n, fmt.Errorf("failed to vacuum: %w", err)
	}
	return n, nil
}
