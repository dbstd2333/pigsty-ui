package configstore

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfig = `---
all:
  children:
    pg-meta:
      hosts:
        10.10.10.10: { pg_seq: 1, pg_role: primary }
      vars:
        pg_cluster: pg-meta
        pg_users:
          - { name: dbuser_meta, password: DBUser.Meta }
  vars:
    version: v4.5.0
    admin_ip: 10.10.10.10
`

// testEnv is a throwaway Pigsty home plus data directory.
type testEnv struct {
	store   *Store
	home    string
	dataDir string
}

// newTestEnv creates a git backed store on temporary directories, so tests
// never touch the real pigsty.yml.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{home: t.TempDir(), dataDir: t.TempDir()}
	if err := os.WriteFile(env.file(), []byte(validConfig), 0o644); err != nil {
		t.Fatalf("write pigsty.yml: %v", err)
	}
	ctx := context.Background()
	repo, err := InitRepo(ctx, env.home, env.gitDir())
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	if _, err := repo.Commit(ctx, "init pigsty.yml", "tester"); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	env.reopen(t)
	return env
}

// reopen opens the store again, e.g. after installing a validate script.
func (e *testEnv) reopen(t *testing.T) *Store {
	t.Helper()
	st, err := Open(Options{
		Home:    e.home,
		DataDir: e.dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	e.store = st
	return st
}

func (e *testEnv) file() string   { return filepath.Join(e.home, "pigsty.yml") }
func (e *testEnv) gitDir() string { return filepath.Join(e.dataDir, "config.git") }

// currentHash returns the sha256 of the file on disk.
func (e *testEnv) currentHash(t *testing.T) string {
	t.Helper()
	_, hash, _, err := e.store.Current(context.Background())
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	return hash
}

// read reads the configuration file from disk.
func (e *testEnv) read(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.file())
	if err != nil {
		t.Fatalf("read pigsty.yml: %v", err)
	}
	return string(data)
}

// totalCommits returns the number of commits in the history.
func (e *testEnv) totalCommits(t *testing.T) int {
	t.Helper()
	_, total, err := e.store.History(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	return total
}

func TestSaveSuccess(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	updated := strings.Replace(validConfig, "admin_ip: 10.10.10.10", "admin_ip: 10.10.10.11", 1)

	result, err := env.store.Save(ctx, SaveRequest{
		Text: updated, BaseHash: env.currentHash(t), Message: "change admin ip", Author: "admin",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if result.Hash != hashText(updated) {
		t.Errorf("hash = %s, want %s", result.Hash, hashText(updated))
	}
	if result.Commit.Author != "admin" || result.Commit.Message != "change admin ip" {
		t.Errorf("unexpected commit: %+v", result.Commit)
	}
	if env.read(t) != updated {
		t.Errorf("file content was not replaced")
	}
	if env.store.PigstyVersion() != "v4.5.0" {
		t.Errorf("pigsty version = %q, want v4.5.0", env.store.PigstyVersion())
	}
	commits, total, err := env.store.History(ctx, 10, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if total != 2 || commits[0].Hash != result.Commit.Hash {
		t.Fatalf("history = %d commits (newest %s), want the new commit", total, commits[0].Hash)
	}
}

func TestSaveConflict(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	updated := strings.Replace(validConfig, "admin_ip: 10.10.10.10", "admin_ip: 10.10.10.11", 1)

	_, err := env.store.Save(ctx, SaveRequest{Text: updated, BaseHash: "deadbeef", Message: "x", Author: "admin"})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want ConflictError", err)
	}
	if conflict.CurrentHash != hashText(validConfig) || conflict.CurrentText != validConfig {
		t.Errorf("conflict payload does not match disk content")
	}
	if env.read(t) != validConfig {
		t.Errorf("file changed despite conflict")
	}
}

func TestSaveUnchangedDoesNotCommit(t *testing.T) {
	env := newTestEnv(t)
	result, err := env.store.Save(context.Background(), SaveRequest{
		Text: validConfig, BaseHash: env.currentHash(t), Message: "noop", Author: "admin",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if result.Commit.Author != "tester" || result.Commit.Message != "init pigsty.yml" {
		t.Errorf("unexpected commit: %+v", result.Commit)
	}
	if total := env.totalCommits(t); total != 1 {
		t.Errorf("history total = %d, want 1", total)
	}
	leftovers, err := filepath.Glob(filepath.Join(env.home, ".pigsty-console-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestSaveValidationFailureFromInventory(t *testing.T) {
	env := newTestEnv(t)
	base := env.currentHash(t)
	// `all.children` is required by the inventory parser and by bin/validate.
	broken := strings.Replace(validConfig, "children:", "not_children:", 1)

	_, err := env.store.Save(context.Background(), SaveRequest{
		Text: broken, BaseHash: base, Message: "broken", Author: "admin",
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
	if !HasErrors(invalid.Issues) {
		t.Fatalf("expected blocking issues, got %+v", invalid.Issues)
	}
	if env.currentHash(t) != base {
		t.Errorf("file changed despite validation failure")
	}
	if total := env.totalCommits(t); total != 1 {
		t.Errorf("history total = %d, want 1", total)
	}
}

func TestValidateTextRunsUpstreamScript(t *testing.T) {
	env := newTestEnv(t)
	script := `#!/usr/bin/env python3
import sys
print("[ OK ] all.vars")
print("[ERRO] 'all.vars.admin_ip' is not a valid ipv4 address")
print("[FAIL] all.vars")
sys.exit(1)
`
	if err := os.MkdirAll(filepath.Join(env.home, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.home, "bin", "validate"), []byte(script), 0o755); err != nil {
		t.Fatalf("write validate: %v", err)
	}
	// The runner probes bin/validate when the store is opened.
	env.reopen(t)
	if !env.store.Validator().Available() {
		t.Skip("python3 is not available, upstream validation cannot run")
	}
	issues, err := env.store.ValidateText(context.Background(), validConfig)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	var found bool
	for _, iss := range issues {
		if iss.Source == "validate" && iss.Path == "all.vars.admin_ip" && iss.Level == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("validate issue not parsed: %+v", issues)
	}
	// The same issues must block a save.
	_, err = env.store.Save(context.Background(), SaveRequest{
		Text:     strings.Replace(validConfig, "admin_ip: 10.10.10.10", "admin_ip: 1.2.3.4", 1),
		BaseHash: env.currentHash(t), Author: "admin",
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
}

func TestSyncExternalCommitsManualEdit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	external := strings.Replace(validConfig, "pg_cluster: pg-meta", "pg_cluster: pg-meta2", 1)
	if err := os.WriteFile(env.file(), []byte(external), 0o644); err != nil {
		t.Fatalf("write external change: %v", err)
	}
	env.store.SyncExternal(ctx)

	commits, total, err := env.store.History(ctx, 10, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if total != 2 {
		t.Fatalf("history total = %d, want 2", total)
	}
	if commits[0].Author != AuthorExternal || commits[0].Message != externalMessage {
		t.Errorf("unexpected commit: %+v", commits[0])
	}
}

func TestWatchCommitsExternalChangeOnly(t *testing.T) {
	env := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := env.store.Watch(ctx); err != nil {
		t.Fatalf("watch: %v", err)
	}

	// A console write must not be reported as an external change.
	if _, err := env.store.Save(ctx, SaveRequest{
		Text:     strings.Replace(validConfig, "version: v4.5.0", "version: v4.5.1", 1),
		BaseHash: env.currentHash(t), Author: "admin",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	time.Sleep(externalDebounce + 500*time.Millisecond)
	if total := env.totalCommits(t); total != 2 {
		t.Fatalf("history total = %d, want 2: own write was treated as external", total)
	}

	// A manual edit is committed as external after the debounce.
	manual := strings.Replace(validConfig, "version: v4.5.0", "version: v4.5.2", 1)
	if err := os.WriteFile(env.file(), []byte(manual), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	deadline := time.Now().Add(externalDebounce + 5*time.Second)
	for time.Now().Before(deadline) {
		commits, total, err := env.store.History(ctx, 10, 0)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if total == 3 {
			if commits[0].Author != AuthorExternal {
				t.Fatalf("author = %q, want external", commits[0].Author)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("external change was not committed within the debounce window")
}

func TestRevertRestoresEarlierContent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	first, _, _, err := env.store.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if _, err := env.store.Save(ctx, SaveRequest{
		Text:     strings.Replace(first, "admin_ip: 10.10.10.10", "admin_ip: 10.10.10.12", 1),
		BaseHash: env.currentHash(t), Message: "change", Author: "admin",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	commits, _, err := env.store.History(ctx, 10, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	oldest := commits[len(commits)-1]
	result, err := env.store.Revert(ctx, oldest.Hash, env.currentHash(t), "", "admin")
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if result.Hash != hashText(first) {
		t.Errorf("revert hash = %s, want %s", result.Hash, hashText(first))
	}
	if !strings.HasPrefix(result.Commit.Message, "revert to ") {
		t.Errorf("message = %q, want the default revert message", result.Commit.Message)
	}
	diff, err := env.store.Diff(ctx, oldest.Hash, "HEAD")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff.FromText != first || diff.ToText != first {
		t.Errorf("diff content does not match the reverted text")
	}
	if diff.Unified != "" {
		t.Errorf("unified diff should be empty, got %q", diff.Unified)
	}
}

func TestValidRef(t *testing.T) {
	for _, ref := range []string{"HEAD", "abc123", strings.Repeat("a", 40)} {
		if !ValidRef(ref) {
			t.Errorf("ValidRef(%q) = false, want true", ref)
		}
	}
	for _, ref := range []string{"", "HEAD~1", "--help", "zzzz", "abc"} {
		if ValidRef(ref) {
			t.Errorf("ValidRef(%q) = true, want false", ref)
		}
	}
}
