package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Job statuses persisted in SQLite.
const (
	JobQueued   = "queued"
	JobRunning  = "running"
	JobSuccess  = "success"
	JobFailed   = "failed"
	JobCanceled = "canceled"
)

// JobRecord is one persisted action run. The JSON columns carry the value types
// of the API contract; the record itself is a plain storage shape.
type JobRecord struct {
	ID           string
	Action       string
	Title        string
	Params       string
	Check        bool
	Risk         string
	Status       string
	Plan         string
	Commands     string
	Cluster      *string
	Targets      string
	ConfigHash   string
	ConfigCommit *string
	User         string
	ConfirmText  *string
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	ExitCode     *int
	CurrentStep  int
	Stats        string
	RestorePoint *string
	Error        *string
	Runner       string
	PID          *int
	Unit         *string
}

// JobFilter selects jobs for the list endpoint.
type JobFilter struct {
	Cluster string
	Status  string
	Limit   int
	Offset  int
}

// InsertJob stores a new job record.
func (s *Store) InsertJob(rec JobRecord) error {
	_, err := s.db.Exec(`INSERT INTO jobs (
        id, action, title, params, check_flag, risk, status, plan, commands, cluster, targets,
        config_hash, config_commit, username, confirm_text, created_at, started_at, finished_at,
        exit_code, current_step, stats, restore_point, error, runner, pid, unit
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Action, rec.Title, rec.Params, boolInt(rec.Check), rec.Risk, rec.Status, rec.Plan,
		rec.Commands, rec.Cluster, rec.Targets, rec.ConfigHash, rec.ConfigCommit, rec.User, rec.ConfirmText,
		formatTime(rec.CreatedAt), formatTimePtr(rec.StartedAt), formatTimePtr(rec.FinishedAt),
		rec.ExitCode, rec.CurrentStep, rec.Stats, rec.RestorePoint, rec.Error, rec.Runner, rec.PID, rec.Unit)
	if err != nil {
		return fmt.Errorf("insert job %s: %w", rec.ID, err)
	}
	return nil
}

// SaveJob replaces the mutable fields of an existing job record.
func (s *Store) SaveJob(rec JobRecord) error {
	res, err := s.db.Exec(`UPDATE jobs SET
            title = ?, status = ?, commands = ?, cluster = ?, targets = ?, config_commit = ?,
            confirm_text = ?, started_at = ?, finished_at = ?, exit_code = ?, current_step = ?,
            stats = ?, restore_point = ?, error = ?, runner = ?, pid = ?, unit = ?
        WHERE id = ?`,
		rec.Title, rec.Status, rec.Commands, rec.Cluster, rec.Targets, rec.ConfigCommit, rec.ConfirmText,
		formatTimePtr(rec.StartedAt), formatTimePtr(rec.FinishedAt), rec.ExitCode, rec.CurrentStep,
		rec.Stats, rec.RestorePoint, rec.Error, rec.Runner, rec.PID, rec.Unit, rec.ID)
	if err != nil {
		return fmt.Errorf("save job %s: %w", rec.ID, err)
	}
	return requireAffected(res, rec.ID)
}

// GetJob returns one job record.
func (s *Store) GetJob(id string) (JobRecord, error) {
	row := s.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	rec, err := scanJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobRecord{}, ErrNotFound
		}
		return JobRecord{}, err
	}
	return rec, nil
}

// ListJobs returns the newest jobs first plus the total count.
func (s *Store) ListJobs(filter JobFilter) ([]JobRecord, int, error) {
	where, args := jobWhere(filter)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + jobColumns + ` FROM jobs` + where + ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(query, append(args, limit, maxInt(filter.Offset, 0))...)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	out := []JobRecord{}
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// RunningJobs returns jobs that are queued or running, oldest first.
func (s *Store) RunningJobs() ([]JobRecord, error) {
	rows, err := s.db.Query(`SELECT `+jobColumns+` FROM jobs WHERE status IN (?, ?) ORDER BY created_at`,
		JobQueued, JobRunning)
	if err != nil {
		return nil, fmt.Errorf("list running jobs: %w", err)
	}
	defer rows.Close()
	out := []JobRecord{}
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// CountJobsWithPrefix counts the jobs created on one day, used for job ids.
func (s *Store) CountJobsWithPrefix(prefix string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id LIKE ?`, prefix+"-%").Scan(&n); err != nil {
		return 0, fmt.Errorf("count jobs: %w", err)
	}
	return n, nil
}

// jobColumns is the shared column list of the job queries.
const jobColumns = `id, action, title, params, check_flag, risk, status, plan, commands, cluster, targets,
    config_hash, config_commit, username, confirm_text, created_at, started_at, finished_at, exit_code,
    current_step, stats, restore_point, error, runner, pid, unit`

// jobWhere builds the list filter clause.
func jobWhere(filter JobFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	if filter.Cluster != "" {
		clauses = append(clauses, "cluster = ?")
		args = append(args, filter.Cluster)
	}
	if filter.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, filter.Status)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// rowScanner is shared by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob reads one job record.
func scanJob(row rowScanner) (JobRecord, error) {
	var rec JobRecord
	var check int
	var createdAt string
	var startedAt, finishedAt, confirm, cluster, commit, restPoint, errText, runner, unit sql.NullString
	var pid, exitCode sql.NullInt64
	if err := row.Scan(&rec.ID, &rec.Action, &rec.Title, &rec.Params, &check, &rec.Risk, &rec.Status,
		&rec.Plan, &rec.Commands, &cluster, &rec.Targets, &rec.ConfigHash, &commit, &rec.User, &confirm,
		&createdAt, &startedAt, &finishedAt, &exitCode, &rec.CurrentStep, &rec.Stats, &restPoint, &errText,
		&runner, &pid, &unit); err != nil {
		return JobRecord{}, err
	}
	rec.Check = check != 0
	rec.Cluster = nullStringPtr(cluster)
	rec.ConfigCommit = nullStringPtr(commit)
	rec.ConfirmText = nullStringPtr(confirm)
	rec.RestorePoint = nullStringPtr(restPoint)
	rec.Error = nullStringPtr(errText)
	rec.Runner = runner.String
	rec.Unit = nullStringPtr(unit)
	if pid.Valid {
		v := int(pid.Int64)
		rec.PID = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		rec.ExitCode = &v
	}
	var err error
	if rec.CreatedAt, err = parseTime(createdAt); err != nil {
		return JobRecord{}, err
	}
	if rec.StartedAt, err = parseTimePtr(startedAt); err != nil {
		return JobRecord{}, err
	}
	if rec.FinishedAt, err = parseTimePtr(finishedAt); err != nil {
		return JobRecord{}, err
	}
	return rec, nil
}

// boolInt converts a boolean into the SQLite representation.
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// maxInt returns the larger of two integers.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// nullStringPtr converts a nullable column into a pointer.
func nullStringPtr(v sql.NullString) *string {
	if !v.Valid || v.String == "" {
		return nil
	}
	out := v.String
	return &out
}

// parseTime parses one stored timestamp.
func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(timeLayout, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", v, err)
	}
	return t, nil
}

// parseTimePtr parses an optional timestamp.
func parseTimePtr(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	t, err := parseTime(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// formatTime renders a timestamp for SQLite.
func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// formatTimePtr renders an optional timestamp.
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}
