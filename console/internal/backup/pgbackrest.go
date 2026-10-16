package backup

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Parsed is the interesting part of `pgbackrest info --output=json`.
type Parsed struct {
	Stanza     string
	Status     *Status
	RepoType   string
	RepoPath   string
	Cipher     string
	Backups    []Backup
	ArchiveMin *string
	ArchiveMax *string
}

// rawInfo mirrors the pgBackRest JSON schema (only the fields the console uses).
type rawInfo []struct {
	Name   string `json:"name"`
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Cipher  string `json:"cipher"`
	Archive []struct {
		Min string `json:"min"`
		Max string `json:"max"`
	} `json:"archive"`
	Repo []struct {
		Type   string `json:"type"`
		Path   string `json:"path"`
		Cipher string `json:"cipher"`
		Status struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
		// The repository section repeats every backup as a label/type summary.
		Backup []struct {
			Label string `json:"label"`
			Type  string `json:"type"`
		} `json:"backup"`
	} `json:"repo"`
	Backup []rawBackup `json:"backup"`
}

// rawBackup mirrors one backup set of the pgBackRest JSON schema.
type rawBackup struct {
	Label     string  `json:"label"`
	Type      string  `json:"type"`
	Prior     *string `json:"prior"`
	Timestamp struct {
		Start float64 `json:"start"`
		Stop  float64 `json:"stop"`
	} `json:"timestamp"`
	LSN struct {
		Start *string `json:"start"`
		Stop  *string `json:"stop"`
	} `json:"lsn"`
	WAL struct {
		Start *string `json:"start"`
		Stop  *string `json:"stop"`
	} `json:"wal"`
	Info struct {
		Size       int64 `json:"size"`
		Delta      int64 `json:"delta"`
		Repository struct {
			Size  int64 `json:"size"`
			Delta int64 `json:"delta"`
		} `json:"repository"`
	} `json:"info"`
	Error bool `json:"error"`
}

// ParseInfo parses the JSON document produced by
// `pgbackrest --stanza=<cls> info --output=json`.
func ParseInfo(data []byte) (Parsed, error) {
	var raw rawInfo
	if err := json.Unmarshal(data, &raw); err != nil {
		return Parsed{}, fmt.Errorf("解析 pgbackrest info 输出失败: %w", err)
	}
	if len(raw) == 0 {
		return Parsed{}, fmt.Errorf("pgbackrest info 输出为空，stanza 可能不存在")
	}
	stanza := raw[0]
	out := Parsed{
		Stanza: stanza.Name,
		Status: &Status{Code: stanza.Status.Code, Message: stanza.Status.Message},
		Cipher: stanza.Cipher,
	}
	for _, repo := range stanza.Repo {
		if out.RepoType == "" {
			out.RepoType, out.RepoPath = repo.Type, repo.Path
			if repo.Cipher != "" {
				out.Cipher = repo.Cipher
			}
			if repo.Status.Code != 0 {
				out.Status = &Status{Code: repo.Status.Code, Message: repo.Status.Message}
			}
		}
	}
	// The stanza level list carries the full backup objects; the repository
	// sections only repeat label/type summaries.
	for _, b := range stanza.Backup {
		out.Backups = append(out.Backups, convertBackup(b))
	}
	if len(out.Backups) == 0 {
		for _, repo := range stanza.Repo {
			for _, b := range repo.Backup {
				out.Backups = append(out.Backups, Backup{Label: b.Label, Type: b.Type})
			}
		}
	}
	sort.SliceStable(out.Backups, func(i, j int) bool { return out.Backups[i].Start.Before(out.Backups[j].Start) })

	mins := make([]string, 0, len(stanza.Archive))
	maxs := make([]string, 0, len(stanza.Archive))
	for _, a := range stanza.Archive {
		if a.Min != "" {
			mins = append(mins, a.Min)
		}
		if a.Max != "" {
			maxs = append(maxs, a.Max)
		}
	}
	for _, b := range out.Backups {
		if b.WALStart != nil {
			mins = append(mins, *b.WALStart)
		}
		if b.WALStop != nil {
			maxs = append(maxs, *b.WALStop)
		}
	}
	if len(mins) > 0 {
		min := mins[0]
		for _, v := range mins[1:] {
			if v < min {
				min = v
			}
		}
		out.ArchiveMin = &min
	}
	if len(maxs) > 0 {
		max := maxs[0]
		for _, v := range maxs[1:] {
			if v > max {
				max = v
			}
		}
		out.ArchiveMax = &max
	}
	return out, nil
}

// convertBackup flattens one raw backup set.
func convertBackup(b rawBackup) Backup {
	out := Backup{
		Label:         b.Label,
		Type:          strings.TrimSpace(b.Type),
		Prior:         b.Prior,
		Start:         epoch(b.Timestamp.Start),
		Stop:          epoch(b.Timestamp.Stop),
		Size:          b.Info.Size,
		DeltaSize:     b.Info.Delta,
		RepoSize:      b.Info.Repository.Size,
		RepoDeltaSize: b.Info.Repository.Delta,
		LSNStart:      b.LSN.Start,
		LSNStop:       b.LSN.Stop,
		WALStart:      b.WAL.Start,
		WALStop:       b.WAL.Stop,
		Error:         b.Error,
	}
	return out
}

// epoch converts a Unix timestamp into UTC, zero when unset.
func epoch(seconds float64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0).UTC()
}

// ComputeWindow returns the recoverable window: from the end of the oldest
// backup to the most recent archived WAL (falling back to the newest backup).
func ComputeWindow(backups []Backup, archive Archive) *Window {
	var start time.Time
	for _, b := range backups {
		if b.Error || b.Stop.IsZero() {
			continue
		}
		if start.IsZero() || b.Stop.Before(start) {
			start = b.Stop
		}
	}
	if start.IsZero() {
		return nil
	}
	end := start
	for _, b := range backups {
		if !b.Error && b.Stop.After(end) {
			end = b.Stop
		}
	}
	if archive.LastArchivedAt != nil && archive.LastArchivedAt.After(end) {
		end = *archive.LastArchivedAt
	}
	return &Window{Start: start, End: end}
}

// LatestBackup returns the newest completed backup, nil when there is none.
func LatestBackup(backups []Backup) *Backup {
	var latest *Backup
	for i := range backups {
		b := backups[i]
		if b.Error {
			continue
		}
		if latest == nil || b.Stop.After(latest.Stop) {
			copied := b
			latest = &copied
		}
	}
	return latest
}

// SelectBackupSet picks the backup set a recovery target depends on: the newest
// backup that finished before the target time, or the newest one overall when
// the target does not name a time.
func SelectBackupSet(backups []Backup, target *time.Time) *Backup {
	var chosen *Backup
	for i := range backups {
		b := backups[i]
		if b.Error {
			continue
		}
		if target != nil && !b.Stop.IsZero() && b.Stop.After(*target) {
			continue
		}
		if chosen == nil || b.Stop.After(chosen.Stop) {
			copied := b
			chosen = &copied
		}
	}
	if chosen == nil && target == nil {
		return LatestBackup(backups)
	}
	return chosen
}
