package backup

import (
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// Repo describes the pgBackRest repository a cluster writes to.
type Repo struct {
	Method    string  `json:"method"`
	Type      string  `json:"type"`
	Path      *string `json:"path"`
	Retention *string `json:"retention"`
	Cipher    *string `json:"cipher"`
}

// Status is the pgBackRest stanza or repository status.
type Status struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Backup is one backup set of the repository.
type Backup struct {
	Label         string    `json:"label"`
	Type          string    `json:"type" enum:"full,diff,incr"`
	Prior         *string   `json:"prior"`
	Start         time.Time `json:"start"`
	Stop          time.Time `json:"stop"`
	Size          int64     `json:"size"`
	DeltaSize     int64     `json:"deltaSize"`
	RepoSize      int64     `json:"repoSize"`
	RepoDeltaSize int64     `json:"repoDeltaSize"`
	LSNStart      *string   `json:"lsnStart"`
	LSNStop       *string   `json:"lsnStop"`
	WALStart      *string   `json:"walStart"`
	WALStop       *string   `json:"walStop"`
	Error         bool      `json:"error"`
}

// Archive summarizes the WAL archive ranges and archiver statistics.
type Archive struct {
	Min            *string    `json:"min"`
	Max            *string    `json:"max"`
	LastArchivedAt *time.Time `json:"lastArchivedAt"`
	FailedCount    *int64     `json:"failedCount"`
	LastFailedAt   *time.Time `json:"lastFailedAt"`
}

// Window is the recoverable window of a cluster.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// RestorePoint is a named PostgreSQL restore point (shared API contract type).
type RestorePoint = plan.RestorePoint

// TimelineEvent marks a PITR run, a failover or another console job on the
// timeline of a cluster.
type TimelineEvent struct {
	Time  time.Time `json:"time"`
	Kind  string    `json:"kind" enum:"pitr,failover,job"`
	Label string    `json:"label"`
	JobID *string   `json:"jobId"`
}

// Info mirrors BackupInfo of the API contract.
type Info struct {
	Cluster       string          `json:"cluster"`
	Source        plan.SourceInfo `json:"source"`
	Stanza        string          `json:"stanza"`
	Repo          Repo            `json:"repo"`
	Status        *Status         `json:"status"`
	Backups       []Backup        `json:"backups"`
	Archive       Archive         `json:"archive"`
	Window        *Window         `json:"window"`
	RestorePoints []RestorePoint  `json:"restorePoints"`
	Events        []TimelineEvent `json:"events"`
	FetchedAt     time.Time       `json:"fetchedAt"`
}

// pgPitrTime matches the timestamp literal shape pg_pitr expects.
const pgPitrTimeLayout = "2006-01-02 15:04:05+00"
