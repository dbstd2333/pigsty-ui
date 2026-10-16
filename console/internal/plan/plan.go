// Package plan holds the types shared by the action catalog, the PITR planner
// and the HTTP layer: risk levels, preconditions and confirmation gates.
package plan

import (
	"fmt"
	"strings"
	"time"
)

// Risk grades an action by how much damage a mistake can cause.
type Risk string

// Risk levels, ordered from harmless to irreversible.
const (
	RiskReadonly Risk = "readonly"
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

// riskRank orders the risk levels for comparisons.
var riskRank = map[Risk]int{
	RiskReadonly: 0,
	RiskLow:      1,
	RiskMedium:   2,
	RiskHigh:     3,
	RiskCritical: 4,
}

// Valid reports whether the risk is one of the defined levels.
func (r Risk) Valid() bool {
	_, ok := riskRank[r]
	return ok
}

// AtLeast reports whether the risk is equal to or higher than other.
func (r Risk) AtLeast(other Risk) bool { return riskRank[r] >= riskRank[other] }

// Upgrade returns the higher of two risk levels.
func (r Risk) Upgrade(other Risk) Risk {
	if r.AtLeast(other) {
		return r
	}
	return other
}

// Precondition is one gate on the road to executing an action. An unmet
// precondition with level "error" blocks submission; "warning" is advisory.
type Precondition struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Level   string `json:"level" enum:"error,warning"`
	Message string `json:"message"`
}

// Levels of a precondition.
const (
	LevelError   = "error"
	LevelWarning = "warning"
)

// Err records an unmet error precondition.
func Err(id, message string) Precondition {
	return Precondition{ID: id, OK: false, Level: LevelError, Message: message}
}

// OK records a met precondition.
func OK(id, message string) Precondition {
	return Precondition{ID: id, OK: true, Level: LevelError, Message: message}
}

// Warn records an unmet advisory precondition.
func Warn(id, message string) Precondition {
	return Precondition{ID: id, OK: false, Level: LevelWarning, Message: message}
}

// Gate collects the extra confirmations required before a risky action runs.
type Gate struct {
	ConfirmText       string     `json:"confirmText"`
	Checkboxes        []string   `json:"checkboxes"`
	LastBackupAt      *time.Time `json:"lastBackupAt"`
	BackupMaxAgeHours int        `json:"backupMaxAgeHours"`
}

// Blocked reports whether any error level precondition failed.
func Blocked(conds []Precondition) bool {
	for _, c := range conds {
		if !c.OK && c.Level == LevelError {
			return true
		}
	}
	return false
}

// FirstError returns the message of the first unmet error precondition.
func FirstError(conds []Precondition) string {
	for _, c := range conds {
		if !c.OK && c.Level == LevelError {
			return c.Message
		}
	}
	return ""
}

// FormatTime renders a timestamp the way pg_pitr expects it in extra vars.
const PitrTimeLayout = "2006-01-02 15:04:05+00"

// FormatPitrTime converts a timestamp into the UTC literal pg_pitr expects.
func FormatPitrTime(t time.Time) string { return t.UTC().Format(PitrTimeLayout) }

// pitrTimeLayouts lists the timestamp shapes the console accepts. A value
// without a zone is interpreted as UTC so the console never guesses a server
// timezone.
var pitrTimeLayouts = []string{
	PitrTimeLayout,
	"2006-01-02 15:04:05-07",
	"2006-01-02 15:04:05.999999999+00",
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05+00",
	"2006-01-02T15:04:05-07",
	"2006-01-02T15:04:05.999999999+00",
	"2006-01-02T15:04:05.999999999-07",
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
}

// ParsePitrTime parses a pg_pitr time literal back into a UTC timestamp.
func ParsePitrTime(s string) (time.Time, error) {
	value := strings.TrimSpace(s)
	for _, layout := range pitrTimeLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q，需为 RFC 3339 或 YYYY-MM-DD HH:MM:SS+00", s)
}

// SourceInfo reports where one piece of external data came from and whether the
// request succeeded. Failures are never fatal: they only show up here.
type SourceInfo struct {
	OK    bool      `json:"ok"`
	URL   string    `json:"url"`
	Error *string   `json:"error"`
	At    time.Time `json:"at"`
}

// NewSource builds a successful source record.
func NewSource(url string) SourceInfo {
	return SourceInfo{OK: true, URL: url, At: time.Now().UTC()}
}

// FailedSource builds a failed source record.
func FailedSource(url, err string) SourceInfo {
	if err == "" {
		err = "取数失败"
	}
	return SourceInfo{OK: false, URL: url, Error: &err, At: time.Now().UTC()}
}

// AdhocSource builds the source record of an ansible ad-hoc collection.
func AdhocSource(host string) string { return "ansible:" + host }

// RestorePoint is a named PostgreSQL restore point created for a job. It is
// shared by the job records and the backup timelines of the API contract.
type RestorePoint struct {
	Name    string    `json:"name"`
	LSN     *string   `json:"lsn"`
	Time    time.Time `json:"time"`
	JobID   *string   `json:"jobId"`
	Cluster string    `json:"cluster"`
}
