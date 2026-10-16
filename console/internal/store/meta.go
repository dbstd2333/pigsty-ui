package store

import (
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// AppliedRow is one successful (non check) job of a cluster.
type AppliedRow struct {
	Cluster string
	Action  string
	Commit  string
	JobID   *string
	At      time.Time
}

// RestorePointRecord is a named PostgreSQL restore point created by the console.
type RestorePointRecord struct {
	Name    string
	LSN     *string
	Time    time.Time
	JobID   *string
	Cluster string
}

// TimelineEventRecord is a console job or timeline switch of one cluster.
type TimelineEventRecord struct {
	Time  time.Time
	Kind  string
	Label string
	JobID *string
}

// NodeSpecRecord is the manually entered capacity of an unmanaged node.
type NodeSpecRecord struct {
	IP        string
	CPU       int
	MemMB     int
	DiskGB    int
	UpdatedAt time.Time
}

// AddApplied records that a cluster was reconciled at one configuration
// version.
func (s *Store) AddApplied(row AppliedRow) error {
	_, err := s.db.Exec(`INSERT INTO applied (cluster, action, commit_hash, job_id, at) VALUES (?, ?, ?, ?, ?)`,
		row.Cluster, row.Action, row.Commit, row.JobID, formatTime(row.At))
	if err != nil {
		return fmt.Errorf("insert applied: %w", err)
	}
	return nil
}

// LatestApplied returns the newest applied row of every cluster.
func (s *Store) LatestApplied() (map[string]AppliedRow, error) {
	rows, err := s.db.Query(`SELECT cluster, action, commit_hash, job_id, at FROM applied
        WHERE id IN (SELECT MAX(id) FROM applied GROUP BY cluster)`)
	if err != nil {
		return nil, fmt.Errorf("list applied: %w", err)
	}
	defer rows.Close()
	out := map[string]AppliedRow{}
	for rows.Next() {
		var row AppliedRow
		var jobID sql.NullString
		var at string
		if err := rows.Scan(&row.Cluster, &row.Action, &row.Commit, &jobID, &at); err != nil {
			return nil, fmt.Errorf("scan applied: %w", err)
		}
		row.JobID = nullStringPtr(jobID)
		if row.At, err = parseTime(at); err != nil {
			return nil, err
		}
		out[row.Cluster] = row
	}
	return out, rows.Err()
}

// AddRestorePoint stores a named restore point, replacing a duplicate name.
func (s *Store) AddRestorePoint(rp RestorePointRecord) error {
	_, err := s.db.Exec(`INSERT INTO restore_points (name, lsn, time, job_id, cluster, created_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET lsn = excluded.lsn, time = excluded.time,
            job_id = excluded.job_id, cluster = excluded.cluster, created_at = excluded.created_at`,
		rp.Name, rp.LSN, formatTime(rp.Time), rp.JobID, rp.Cluster, formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("insert restore point %s: %w", rp.Name, err)
	}
	return nil
}

// RestorePoints returns the restore points of one cluster, newest first.
func (s *Store) RestorePoints(cluster string) ([]RestorePointRecord, error) {
	rows, err := s.db.Query(`SELECT name, lsn, time, job_id, cluster FROM restore_points
        WHERE cluster = ? ORDER BY time DESC`, cluster)
	if err != nil {
		return nil, fmt.Errorf("list restore points: %w", err)
	}
	defer rows.Close()
	out := []RestorePointRecord{}
	for rows.Next() {
		var rp RestorePointRecord
		var lsn, jobID sql.NullString
		var at string
		if err := rows.Scan(&rp.Name, &lsn, &at, &jobID, &rp.Cluster); err != nil {
			return nil, fmt.Errorf("scan restore point: %w", err)
		}
		rp.LSN = nullStringPtr(lsn)
		rp.JobID = nullStringPtr(jobID)
		if rp.Time, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, rp)
	}
	return out, rows.Err()
}

// TimelineEvents returns the PITR runs and high risk jobs of one cluster,
// newest first.
func (s *Store) TimelineEvents(cluster string) ([]TimelineEventRecord, error) {
	rows, err := s.db.Query(`SELECT id, action, title, finished_at FROM jobs
        WHERE cluster = ? AND status = ? AND finished_at IS NOT NULL
          AND (action LIKE 'pitr-%' OR risk IN ('high', 'critical'))
        ORDER BY finished_at DESC LIMIT 50`, cluster, JobSuccess)
	if err != nil {
		return nil, fmt.Errorf("list timeline events: %w", err)
	}
	defer rows.Close()
	out := []TimelineEventRecord{}
	for rows.Next() {
		var id, action, title, at string
		if err := rows.Scan(&id, &action, &title, &at); err != nil {
			return nil, fmt.Errorf("scan timeline event: %w", err)
		}
		when, err := parseTime(at)
		if err != nil {
			return nil, err
		}
		kind := "job"
		if len(action) > 5 && action[:5] == "pitr-" {
			kind = "pitr"
		}
		jobID := id
		out = append(out, TimelineEventRecord{Time: when, Kind: kind, Label: title, JobID: &jobID})
	}
	return out, rows.Err()
}

// UpsertNodeSpec stores the manually entered capacity of one node.
func (s *Store) UpsertNodeSpec(spec NodeSpecRecord) error {
	_, err := s.db.Exec(`INSERT INTO node_specs (ip, cpu, mem_mb, disk_gb, updated_at) VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(ip) DO UPDATE SET cpu = excluded.cpu, mem_mb = excluded.mem_mb,
            disk_gb = excluded.disk_gb, updated_at = excluded.updated_at`,
		spec.IP, spec.CPU, spec.MemMB, spec.DiskGB, formatTime(spec.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert node spec %s: %w", spec.IP, err)
	}
	return nil
}

// NodeSpecs returns all node specs sorted by IP.
func (s *Store) NodeSpecs() ([]NodeSpecRecord, error) {
	rows, err := s.db.Query(`SELECT ip, cpu, mem_mb, disk_gb, updated_at FROM node_specs`)
	if err != nil {
		return nil, fmt.Errorf("list node specs: %w", err)
	}
	defer rows.Close()
	out := []NodeSpecRecord{}
	for rows.Next() {
		var spec NodeSpecRecord
		var at string
		if err := rows.Scan(&spec.IP, &spec.CPU, &spec.MemMB, &spec.DiskGB, &at); err != nil {
			return nil, fmt.Errorf("scan node spec: %w", err)
		}
		if spec.UpdatedAt, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out, rows.Err()
}

// DeleteNodeSpec removes one node spec, reporting ErrNotFound when absent.
func (s *Store) DeleteNodeSpec(ip string) error {
	res, err := s.db.Exec(`DELETE FROM node_specs WHERE ip = ?`, ip)
	if err != nil {
		return fmt.Errorf("delete node spec %s: %w", ip, err)
	}
	return requireAffected(res, ip)
}

// Settings returns all stored settings as key/value pairs.
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		out[key] = value
	}
	return out, rows.Err()
}

// PutSettings upserts the given settings in one transaction.
func (s *Store) PutSettings(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin settings tx: %w", err)
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	for key, value := range values {
		if _, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
            ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now); err != nil {
			return fmt.Errorf("upsert setting %s: %w", key, err)
		}
	}
	return tx.Commit()
}
