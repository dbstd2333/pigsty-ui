package backup

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// simulateDays is the number of days of fake backup history generated.
const simulateDays = 7

// simulateInfo builds the deterministic backup view of the simulate mode: one
// backup per day (full every third day, incremental otherwise), a WAL archive
// range and a matching recoverable window.
func (s *Service) simulateInfo(ctx context.Context, cluster string, info Info) Info {
	seed := hashOf(cluster)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, now.Location())

	size := int64(1+seed%12) * 1024 * 1024 * 1024 // 1-12 GiB of database
	backups := make([]Backup, 0, simulateDays)
	lastFull := -99
	for day := simulateDays - 1; day >= 0; day-- {
		// idx counts days from the oldest backup, so WAL positions grow with
		// time exactly like they do on a real cluster.
		idx := simulateDays - 1 - day
		start := today.AddDate(0, 0, -day).Add(time.Duration(seed%20) * time.Minute)
		if day == 0 {
			// Today's run only happened when it is past 01:00 local time.
			if now.Before(start.Add(30 * time.Minute)) {
				continue
			}
		}
		// One full backup every three days, incremental backups in between,
		// which is what an unparameterised /pg/bin/pg-backup run produces.
		kind := "incr"
		if lastFull-day >= 3 || len(backups) == 0 {
			kind = "full"
			lastFull = day
		}
		duration := time.Duration(2+seed%6) * time.Minute
		if kind != "full" {
			duration = time.Duration(20+seed%40) * time.Second
		}
		deltaSize := size - int64(len(backups))*int64(seed%97)*1024*1024
		if deltaSize <= 0 {
			deltaSize = size / 10
		}
		repoSize := size / 4
		if kind != "full" {
			repoSize = size/4 + deltaSize/20
		}
		prior := ""
		if kind != "full" && len(backups) > 0 {
			prior = backups[len(backups)-1].Label
		}
		label := fmt.Sprintf("%s-%s", start.Format("20060102-150405"), suffix(kind))
		startLSN := fmt.Sprintf("0/%X00", 100+idx*7)
		stopLSN := fmt.Sprintf("0/%X00", 100+idx*7+5)
		walStart := fmt.Sprintf("%08X%08X%08X", 1, 0, 100+idx*7)
		walStop := fmt.Sprintf("%08X%08X%08X", 1, 0, 105+idx*7)
		b := Backup{
			Label:         label,
			Type:          kind,
			Start:         start.UTC(),
			Stop:          start.Add(duration).UTC(),
			Size:          size,
			DeltaSize:     deltaSize,
			RepoSize:      repoSize,
			RepoDeltaSize: repoSize / 3,
			LSNStart:      ptr(startLSN),
			LSNStop:       ptr(stopLSN),
			WALStart:      ptr(walStart),
			WALStop:       ptr(walStop),
		}
		if prior != "" {
			b.Prior = ptr(prior)
		}
		backups = append(backups, b)
	}

	lastArchived := now.Add(-2 * time.Minute).UTC()
	failed := int64(0)
	archive := Archive{
		LastArchivedAt: &lastArchived,
		FailedCount:    &failed,
	}
	if len(backups) > 0 {
		first := backups[0]
		last := backups[len(backups)-1]
		archive.Min = first.WALStart
		archive.Max = last.WALStop
	}
	info.Source = plan.NewSource("simulate://pgbackrest/" + cluster)
	info.Backups = backups
	info.Archive = archive
	info.Status = &Status{Code: 0, Message: "ok"}
	if info.Repo.Retention == nil && info.Repo.Method == "local" {
		retention := "full: 2"
		info.Repo.Retention = &retention
	}
	if info.Repo.Path == nil && info.Repo.Method == "local" {
		info.Repo.Path = ptr("/pg/backup")
	}
	info.Window = ComputeWindow(backups, archive)
	if events, err := s.simulateEvents(ctx, cluster, seed); err == nil {
		info.Events = append(info.Events, events...)
	}
	return info
}

// simulateEvents adds a timeline switch to clusters that already failed over.
func (s *Service) simulateEvents(_ context.Context, cluster string, seed int64) ([]TimelineEvent, error) {
	if seed%2 == 0 {
		return nil, nil
	}
	at := time.Now().AddDate(0, 0, -4).Truncate(time.Minute).UTC()
	return []TimelineEvent{{Time: at, Kind: "failover", Label: cluster + " 发生一次主从切换（时间线 +1）"}}, nil
}

// hashOf derives a stable seed from a name.
func hashOf(v string) int64 {
	h := fnv.New64a()
	h.Write([]byte(v))
	return int64(h.Sum64() % 1000)
}

// ptr returns a pointer to a copy of v.
func ptr[T any](v T) *T { return &v }

// suffix returns the pgBackRest backup label suffix of a backup type.
func suffix(kind string) string {
	switch kind {
	case "full":
		return "F"
	case "diff":
		return "D"
	default:
		return "I"
	}
}
