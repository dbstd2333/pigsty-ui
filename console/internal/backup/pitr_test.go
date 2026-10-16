package backup

import (
	"testing"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// testInfo builds a backup view equivalent to the sample JSON plus a restore
// point inside the window.
func testInfo(t *testing.T) Info {
	t.Helper()
	parsed, err := ParseInfo([]byte(sampleInfo))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	archived := time.Unix(1792130000, 0).UTC()
	last := int64(0)
	info := Info{
		Cluster: "pg-meta",
		Stanza:  "pg-meta",
		Repo:    Repo{Method: "minio", Type: "s3", Retention: strPtr("time: 14 days")},
		Backups: parsed.Backups,
		Archive: Archive{Min: parsed.ArchiveMin, Max: parsed.ArchiveMax, LastArchivedAt: &archived, FailedCount: &last},
	}
	jobID := "20261016-0001"
	info.RestorePoints = []RestorePoint{{
		Name: "before_upgrade", LSN: strPtr("0/9000000"),
		Time: parsed.Backups[0].Stop.Add(2 * time.Hour), JobID: &jobID, Cluster: "pg-meta",
	}}
	info.Window = ComputeWindow(info.Backups, info.Archive)
	return info
}

func strPtr(v string) *string { return &v }

func TestAssessPitrInplace(t *testing.T) {
	info := testInfo(t)
	target, err := ParseTarget(RawTarget{"type": "time", "time": "2026-10-14 12:00:00+08"})
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	assessment := AssessPitr(AssessmentOptions{
		Cluster: "pg-meta", Mode: "inplace", Target: target, Action: "pause", Backup: true, Info: info,
		TargetHosts: []string{"10.10.10.10"}, DiskUsage: func(string) (int64, int64, bool) {
			return 100 << 30, 8 << 30, true
		},
	})
	if !assessment.InWindow {
		t.Fatalf("target must be inside the window: %+v", assessment.Preconditions)
	}
	if assessment.BackupSet == nil || *assessment.BackupSet != info.Backups[0].Label {
		t.Fatalf("backup set = %v, want %s", assessment.BackupSet, info.Backups[0].Label)
	}
	if assessment.LossWindow == nil {
		t.Fatal("in-place restore must report the loss window")
	}
	if !assessment.LossWindow.From.Equal(info.Backups[0].Stop.Add(-22*time.Hour)) &&
		assessment.LossWindow.From.UTC().Hour() != 4 {
		t.Fatalf("loss window from = %s", assessment.LossWindow.From)
	}
	if assessment.LossWindow.To.Before(assessment.LossWindow.From) {
		t.Fatalf("loss window is inverted: %+v", assessment.LossWindow)
	}
	if assessment.EstimatedSeconds == nil || *assessment.EstimatedSeconds <= 0 {
		t.Fatalf("estimated seconds = %v", assessment.EstimatedSeconds)
	}
	if assessment.Gate == nil || assessment.Gate.ConfirmText != "pg-meta" {
		t.Fatalf("gate = %+v", assessment.Gate)
	}
	if plan.Blocked(assessment.Preconditions) {
		t.Fatalf("plan must not be blocked: %+v", assessment.Preconditions)
	}
}

func TestAssessPitrOutsideWindow(t *testing.T) {
	info := testInfo(t)
	target, err := ParseTarget(RawTarget{"type": "time", "time": "2026-10-20 00:00:00+00"})
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	assessment := AssessPitr(AssessmentOptions{
		Cluster: "pg-meta", Mode: "inplace", Target: target, Info: info, TargetHosts: []string{"10.10.10.10"},
	})
	if assessment.InWindow {
		t.Fatal("target after the window must not be in window")
	}
	if !plan.Blocked(assessment.Preconditions) {
		t.Fatalf("target after the window must block: %+v", assessment.Preconditions)
	}
}

func TestAssessPitrBeforeFirstBackup(t *testing.T) {
	info := testInfo(t)
	target, err := ParseTarget(RawTarget{"type": "time", "time": "2026-10-01 00:00:00+00"})
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	assessment := AssessPitr(AssessmentOptions{
		Cluster: "pg-meta", Mode: "inplace", Target: target, Info: info, TargetHosts: []string{"10.10.10.10"},
	})
	if assessment.BackupSet != nil {
		t.Fatalf("no backup set exists before the first backup, got %v", *assessment.BackupSet)
	}
	if !plan.Blocked(assessment.Preconditions) {
		t.Fatalf("must block without a usable backup set: %+v", assessment.Preconditions)
	}
}

func TestAssessPitrLocalRepoClone(t *testing.T) {
	info := testInfo(t)
	info.Repo = Repo{Method: "local", Type: "posix", Path: strPtr("/pg/backup")}
	target, err := ParseTarget(RawTarget{"type": "latest"})
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	remote := AssessPitr(AssessmentOptions{
		Cluster: "pg-meta", Mode: "clone", Target: target, Info: info,
		TargetHosts: []string{"10.10.10.11"}, SourcePrimary: "10.10.10.10",
	})
	if !plan.Blocked(remote.Preconditions) {
		t.Fatalf("a local repository must block a clone onto another node: %+v", remote.Preconditions)
	}
	if remote.Gate != nil {
		t.Fatalf("clone mode must not require the in-place gate: %+v", remote.Gate)
	}
	sameHost := AssessPitr(AssessmentOptions{
		Cluster: "pg-meta", Mode: "clone", Target: target, Info: info,
		TargetHosts: []string{"10.10.10.10"}, SourcePrimary: "10.10.10.10",
	})
	if plan.Blocked(sameHost.Preconditions) {
		t.Fatalf("same host clone must pass: %+v", sameHost.Preconditions)
	}
}

func TestAssessPitrDiskSpace(t *testing.T) {
	info := testInfo(t)
	target, err := ParseTarget(RawTarget{"type": "latest"})
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	low := AssessPitr(AssessmentOptions{
		Cluster: "pg-meta", Mode: "inplace", Target: target, Backup: true, Info: info,
		TargetHosts: []string{"10.10.10.10"},
		DiskUsage:   func(string) (int64, int64, bool) { return 1 << 30, 8 << 30, true },
	})
	if !plan.Blocked(low.Preconditions) {
		t.Fatalf("insufficient disk space must block: %+v", low.Preconditions)
	}
	unknown := AssessPitr(AssessmentOptions{
		Cluster: "pg-meta", Mode: "inplace", Target: target, Backup: true, Info: info,
		TargetHosts: []string{"10.10.10.10"},
		DiskUsage:   func(string) (int64, int64, bool) { return 0, 0, false },
	})
	if plan.Blocked(unknown.Preconditions) {
		t.Fatalf("missing metrics must only warn: %+v", unknown.Preconditions)
	}
	found := false
	for _, cond := range unknown.Preconditions {
		if cond.ID == "disk_space" && cond.Level == plan.LevelWarning {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a disk space warning: %+v", unknown.Preconditions)
	}
}

func TestPgPitrRendering(t *testing.T) {
	cases := []struct {
		raw    RawTarget
		want   map[string]any
		absent []string
	}{
		{RawTarget{"type": "latest"}, map[string]any{}, []string{"type", "time", "name"}},
		{RawTarget{"type": "time", "time": "2026-01-01T10:00:00+08:00"},
			map[string]any{"type": "time", "time": "2026-01-01 02:00:00+00"}, []string{"lsn"}},
		{RawTarget{"type": "name", "name": "before_upgrade"},
			map[string]any{"type": "name", "name": "before_upgrade"}, []string{"time"}},
		{RawTarget{"type": "lsn", "lsn": "0/4001c80", "exclusive": true},
			map[string]any{"type": "lsn", "lsn": "0/4001C80", "exclusive": true}, []string{"time"}},
		{RawTarget{"type": "xid", "xid": "250000"},
			map[string]any{"type": "xid", "xid": "250000"}, []string{"lsn"}},
		{RawTarget{"type": "immediate"}, map[string]any{"type": "immediate"}, []string{"set"}},
	}
	for _, tc := range cases {
		target, err := ParseTarget(tc.raw)
		if err != nil {
			t.Fatalf("ParseTarget(%v): %v", tc.raw, err)
		}
		got := PgPitr(target, "pause", false, "")
		for key, want := range tc.want {
			if got[key] != want {
				t.Fatalf("pg_pitr[%s] = %v, want %v", key, got[key], want)
			}
		}
		for _, key := range tc.absent {
			if _, ok := got[key]; ok {
				t.Fatalf("pg_pitr must not contain %s: %v", key, got)
			}
		}
	}

	target, err := ParseTarget(RawTarget{"type": "time", "time": "2026-01-01 10:00:00+00", "set": "20251225-120000F"})
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	clone := PgPitr(target, "promote", true, "pg-meta")
	if clone["cluster"] != "pg-meta" || clone["action"] != "promote" || clone["backup"] != true {
		t.Fatalf("clone payload = %v", clone)
	}
	if clone["set"] != "20251225-120000F" {
		t.Fatalf("set = %v", clone["set"])
	}
	json := PgPitrJSON(map[string]any{"pg_pitr": clone})
	if json == "" || json[0] != '{' {
		t.Fatalf("json = %s", json)
	}
}

func TestParseTargetRejectsBadValues(t *testing.T) {
	bad := []RawTarget{
		{"type": "nope"},
		{"type": "time"},
		{"type": "time", "time": "yesterday"},
		{"type": "lsn", "lsn": "not-an-lsn"},
		{"type": "xid", "xid": "-1"},
		{"type": "name", "name": "bad name"},
	}
	for _, raw := range bad {
		if _, err := ParseTarget(raw); err == nil {
			t.Fatalf("ParseTarget(%v) = nil, want error", raw)
		}
	}
}

func TestParsePitrTimeFormats(t *testing.T) {
	cases := map[string]string{
		"2026-10-09T02:00:00+00":    "2026-10-09 02:00:00+00",
		"2026-10-09T10:00:00+08:00": "2026-10-09 02:00:00+00",
		"2026-10-09 10:00:00+08":    "2026-10-09 02:00:00+00",
		"2026-10-09 02:00:00+00":    "2026-10-09 02:00:00+00",
		"2026-10-09 02:00:00":       "2026-10-09 02:00:00+00",
		"2026-10-09T02:00:00.123Z":  "2026-10-09 02:00:00+00",
	}
	for input, want := range cases {
		got, err := plan.ParsePitrTime(input)
		if err != nil {
			t.Fatalf("ParsePitrTime(%q): %v", input, err)
		}
		if formatted := plan.FormatPitrTime(got); formatted != want {
			t.Fatalf("ParsePitrTime(%q) = %s, want %s", input, formatted, want)
		}
	}
	if _, err := plan.ParsePitrTime("yesterday"); err == nil {
		t.Fatal("garbage must be rejected")
	}
}

func TestSimulatedWindowGrowsWithTime(t *testing.T) {
	service := New(Options{Simulate: true, Fetcher: nil})
	info := service.simulateInfo(nil, "pg-meta", Info{Cluster: "pg-meta", Stanza: "pg-meta"})
	if len(info.Backups) < 3 {
		t.Fatalf("backups = %d", len(info.Backups))
	}
	for i := 1; i < len(info.Backups); i++ {
		prev, cur := info.Backups[i-1], info.Backups[i]
		if !cur.Start.After(prev.Start) {
			t.Fatalf("backups are not ordered: %s then %s", prev.Start, cur.Start)
		}
		if cur.WALStart == nil || prev.WALStart == nil {
			t.Fatal("simulated backups need WAL ranges")
		}
		if *cur.WALStart <= *prev.WALStart {
			t.Fatalf("WAL range must grow with time: %s then %s", *prev.WALStart, *cur.WALStart)
		}
		if *cur.WALStop < *prev.WALStop {
			t.Fatalf("WAL stop must not go backwards: %s then %s", *prev.WALStop, *cur.WALStop)
		}
	}
	if info.Archive.Min == nil || info.Archive.Max == nil || *info.Archive.Min >= *info.Archive.Max {
		t.Fatalf("archive range = %v ~ %v", info.Archive.Min, info.Archive.Max)
	}
	if info.Window == nil || !info.Window.End.After(info.Window.Start) {
		t.Fatalf("window = %+v", info.Window)
	}
	full := 0
	for _, b := range info.Backups {
		if b.Type == "full" {
			full++
		}
	}
	if full < 2 {
		t.Fatalf("expected repeated full backups, got %d", full)
	}
}
