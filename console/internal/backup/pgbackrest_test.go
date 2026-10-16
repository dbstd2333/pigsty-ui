package backup

import (
	"testing"
	"time"
)

// sampleInfo is a realistic `pgbackrest --stanza=pg-meta info --output=json`
// document: one full backup, one incremental backup on top of it, plus the WAL
// archive range of the stanza.
const sampleInfo = `[
  {
    "archive": [
      {
        "database": { "id": 1 },
        "id": "18-1",
        "max": "00000001000000000000000B",
        "min": "000000010000000000000003"
      }
    ],
    "backup": [
      {
        "archive": { "start": "000000010000000000000003", "stop": "00000001000000000000000B" },
        "backrest": { "format": 5, "version": "2.55.1" },
        "database": { "id": 1, "repo-key": 1 },
        "error": false,
        "info": {
          "delta": 8589934592,
          "repository": { "delta": 2147483648, "size": 2147483648 },
          "size": 8589934592
        },
        "label": "20261013-010001F",
        "lsn": { "start": "0/5000028", "stop": "0/5000138" },
        "prior": null,
        "reference": [],
        "timestamp": { "start": 1791853201, "stop": 1791853380 },
        "type": "full",
        "wal": { "start": "000000010000000000000005", "stop": "000000010000000000000006" }
      },
      {
        "archive": { "start": "00000001000000000000000A", "stop": "00000001000000000000000B" },
        "backrest": { "format": 5, "version": "2.55.1" },
        "database": { "id": 1, "repo-key": 1 },
        "error": false,
        "info": {
          "delta": 536870912,
          "repository": { "delta": 134217728, "size": 2281701376 },
          "size": 8589934592
        },
        "label": "20261016-010001I",
        "lsn": { "start": "0/A000028", "stop": "0/A000138" },
        "prior": "20261013-010001F",
        "reference": [ "20261013-010001F" ],
        "timestamp": { "start": 1792112401, "stop": 1792112460 },
        "type": "incr",
        "wal": { "start": "00000001000000000000000A", "stop": "00000001000000000000000B" }
      }
    ],
    "cipher": "none",
    "db": [
      { "id": 1, "repo-key": 1, "system-id": "7231863221525369986", "version": "18" }
    ],
    "name": "pg-meta",
    "repo": [
      {
        "backup": [
          { "label": "20261013-010001F", "type": "full" },
          { "label": "20261016-010001I", "type": "incr" }
        ],
        "cipher": "none",
        "delta": 2281701376,
        "id": 1,
        "key": 1,
        "repo-key": 1,
        "size": 4429185024,
        "status": { "code": 0, "message": "ok" },
        "type": "posix",
        "path": "/pg/backup"
      }
    ],
    "status": { "code": 0, "message": "ok" }
  }
]`

func TestParseInfo(t *testing.T) {
	parsed, err := ParseInfo([]byte(sampleInfo))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if parsed.Stanza != "pg-meta" {
		t.Fatalf("stanza = %q, want pg-meta", parsed.Stanza)
	}
	if parsed.Status == nil || parsed.Status.Code != 0 {
		t.Fatalf("status = %+v, want code 0", parsed.Status)
	}
	if parsed.RepoType != "posix" || parsed.RepoPath != "/pg/backup" {
		t.Fatalf("repo = %q %q", parsed.RepoType, parsed.RepoPath)
	}
	if len(parsed.Backups) != 2 {
		t.Fatalf("backups = %d, want 2", len(parsed.Backups))
	}
	first, second := parsed.Backups[0], parsed.Backups[1]
	if first.Type != "full" || second.Type != "incr" {
		t.Fatalf("backup types = %q, %q", first.Type, second.Type)
	}
	if !first.Start.Before(second.Start) {
		t.Fatalf("backups are not sorted by start time")
	}
	if second.Prior == nil || *second.Prior != first.Label {
		t.Fatalf("incr prior = %v, want %s", second.Prior, first.Label)
	}
	if second.RepoSize != 2281701376 || second.RepoDeltaSize != 134217728 {
		t.Fatalf("repo sizes = %d/%d", second.RepoSize, second.RepoDeltaSize)
	}
	wantStart := time.Unix(1791853201, 0).UTC()
	if !first.Start.Equal(wantStart) {
		t.Fatalf("full start = %s, want %s", first.Start, wantStart)
	}
	if parsed.ArchiveMin == nil || *parsed.ArchiveMin != "000000010000000000000003" {
		t.Fatalf("archive min = %v", parsed.ArchiveMin)
	}
	if parsed.ArchiveMax == nil || *parsed.ArchiveMax != "00000001000000000000000B" {
		t.Fatalf("archive max = %v", parsed.ArchiveMax)
	}
}

func TestParseInfoRejectsGarbage(t *testing.T) {
	if _, err := ParseInfo([]byte("pgbackrest: command not found")); err == nil {
		t.Fatal("want error for non JSON output")
	}
	if _, err := ParseInfo([]byte("[]")); err == nil {
		t.Fatal("want error for an empty stanza list")
	}
}

func TestComputeWindowAndSelection(t *testing.T) {
	parsed, err := ParseInfo([]byte(sampleInfo))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	archived := time.Unix(1792130000, 0).UTC()
	archive := Archive{LastArchivedAt: &archived, FailedCount: int64Ptr(0)}
	window := ComputeWindow(parsed.Backups, archive)
	if window == nil {
		t.Fatal("window is nil")
	}
	if !window.Start.Equal(parsed.Backups[0].Stop) {
		t.Fatalf("window start = %s, want %s", window.Start, parsed.Backups[0].Stop)
	}
	if !window.End.Equal(archived) {
		t.Fatalf("window end = %s, want the last archived time", window.End)
	}

	before := parsed.Backups[0].Stop.Add(-time.Hour)
	if set := SelectBackupSet(parsed.Backups, &before); set != nil {
		t.Fatalf("expected no backup set before the first backup, got %s", set.Label)
	}
	after := parsed.Backups[1].Stop.Add(time.Hour)
	if set := SelectBackupSet(parsed.Backups, &after); set == nil || set.Label != parsed.Backups[1].Label {
		t.Fatalf("expected the newest backup set, got %v", set)
	}
	if set := SelectBackupSet(parsed.Backups, nil); set == nil || set.Label != parsed.Backups[1].Label {
		t.Fatalf("expected the newest backup set for a targetless request, got %v", set)
	}
}

func TestComputeWindowWithoutBackups(t *testing.T) {
	if window := ComputeWindow(nil, Archive{}); window != nil {
		t.Fatalf("window = %+v, want nil", window)
	}
}

func int64Ptr(v int64) *int64 { return &v }
