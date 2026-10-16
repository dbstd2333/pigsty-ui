package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// fakePlanner returns a canned plan for the job tests.
type fakePlanner struct {
	mu     sync.Mutex
	plans  map[string]*actions.Plan
	calls  int
	errFor string
}

// Plan implements the Planner interface of the job manager.
func (f *fakePlanner) Plan(_ context.Context, actionID string, params map[string]any, check bool) (*actions.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.errFor == actionID {
		return nil, &actions.ParamError{Issues: nil, Detail: "参数校验失败"}
	}
	if canned, ok := f.plans[actionID]; ok {
		out := *canned
		out.Check = check
		out.Params = params
		return &out, nil
	}
	cluster := "pg-test"
	return &actions.Plan{
		Action:     actionID,
		Title:      actionID + " @ " + cluster,
		Risk:       plan.RiskLow,
		Check:      check,
		Cluster:    &cluster,
		Targets:    []string{"10.10.10.11"},
		ConfigHash: "hash-1",
		Commands:   []string{"./pgsql.yml -l pg-test -t pg_service"},
		Steps: []actions.Command{{
			Argv:    []string{"./pgsql.yml", "-l", "pg-test", "-t", "pg_service"},
			Display: "./pgsql.yml -l pg-test -t pg_service", Kind: "playbook",
			Playbook: "pgsql.yml", Hosts: []string{"10.10.10.11"},
		}},
	}, nil
}

// testManager builds a simulated job manager on a temporary database.
func testManager(t *testing.T, planner Planner, speed float64) (*Manager, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "console.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(Options{
		Home: dir, DataDir: dir, Logger: logger, Store: db, Planner: planner,
		Simulate: true, SimulateSpeed: speed,
	})
	return manager, db
}

// waitFor blocks until a job reached a terminal state.
func waitFor(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	select {
	case <-m.Done(id):
	case <-time.After(30 * time.Second):
		t.Fatalf("job %s did not finish", id)
	}
	job, err := m.Get(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return job
}

func TestSimulatedJobEventSequence(t *testing.T) {
	planner := &fakePlanner{}
	manager, _ := testManager(t, planner, 200)
	job, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-svc", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	history, ch, cancel, err := manager.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()
	if len(history) > 0 {
		t.Fatalf("history should be empty before the first poll, got %d", len(history))
	}
	events := []JobEvent{}
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				goto done
			}
			events = append(events, ev)
			if ev.Type == EventStatus && ev.Job != nil && terminal(ev.Job.Status) {
				goto done
			}
		case <-deadline:
			t.Fatal("timed out waiting for the event stream")
		}
	}
done:
	waitFor(t, manager, job.ID)
	if len(events) < 8 {
		t.Fatalf("event stream is too short: %d events", len(events))
	}
	seen := map[string]int{}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, i+1)
		}
		if ev.TS.IsZero() {
			t.Fatalf("event %d has no timestamp", i)
		}
		seen[ev.Type]++
	}
	for _, want := range []string{EventStepStart, EventStepEnd, EventPlaybookStart, EventPlayStart, EventTaskStart,
		EventRunnerResult, EventStats, EventStatus, EventLog} {
		if seen[want] == 0 {
			t.Fatalf("event type %s is missing: %v", want, seen)
		}
	}
	final := events[len(events)-1]
	if final.Type != EventStatus || final.Job == nil || final.Job.Status != store.JobSuccess {
		t.Fatalf("last event = %+v", final)
	}
	if final.Job.ExitCode == nil || *final.Job.ExitCode != 0 {
		t.Fatalf("exit code = %v", final.Job.ExitCode)
	}
	stats := map[string]HostStats{}
	for _, ev := range events {
		if ev.Type == EventStats {
			for host, item := range ev.Hosts {
				stats[host] = item
			}
		}
	}
	got, ok := stats["10.10.10.11"]
	if !ok {
		t.Fatalf("stats = %+v, want the target host", stats)
	}
	if got.OK+got.Changed+got.Skipped == 0 {
		t.Fatalf("host stats are empty: %+v", got)
	}
	if jobs, total, err := manager.List(JobFilter{}); err != nil || total != 1 || len(jobs) != 1 {
		t.Fatalf("list = %d/%d err %v", len(jobs), total, err)
	}
}

func TestSimulatedJobFailure(t *testing.T) {
	manager, _ := testManager(t, &fakePlanner{}, 200)
	job, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-svc", Params: map[string]any{"cluster": "pg-test", "fail": true}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if final.Status != store.JobFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	if final.ExitCode == nil || *final.ExitCode == 0 {
		t.Fatalf("exit code = %v", final.ExitCode)
	}
	if final.Error == nil {
		t.Fatal("failed job must carry an error message")
	}
}

func TestCancelSimulatedJob(t *testing.T) {
	manager, _ := testManager(t, &fakePlanner{}, 0.02)
	job, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-svc", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if current, err := manager.Get(job.ID); err == nil && current.Status == store.JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := manager.Cancel(context.Background(), job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if final.Status != store.JobCanceled {
		t.Fatalf("status = %s, want canceled", final.Status)
	}
	if final.FinishedAt == nil {
		t.Fatal("canceled job must have a finish time")
	}
}

func TestSingleMutatingJobLock(t *testing.T) {
	planner := &fakePlanner{plans: map[string]*actions.Plan{
		"pitr-inplace": {
			Action: "pitr-inplace", Title: "原地 PITR 恢复 @ pg-test", Risk: plan.RiskCritical,
			Cluster: strPtr("pg-test"), Targets: []string{"10.10.10.11"}, ConfigHash: "hash-1",
			Commands: []string{"./pgsql-pitr.yml -l pg-test -e '{\"pg_pitr\":{}}'"},
			Steps: []actions.Command{{
				Argv:    []string{"./pgsql-pitr.yml", "-l", "pg-test", "-e", `{"pg_pitr":{}}`},
				Display: "./pgsql-pitr.yml -l pg-test", Kind: "pitr", Playbook: "pgsql-pitr.yml",
				Hosts: []string{"10.10.10.11"},
			}},
		},
	}}
	manager, _ := testManager(t, planner, 0.02)
	first, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pitr-inplace", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	defer func() {
		manager.Cancel(context.Background(), first.ID)
		waitFor(t, manager, first.ID)
	}()

	_, err = manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-svc", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("second submit error = %v, want ConflictError", err)
	}
	if conflict.RunningJobID != first.ID {
		t.Fatalf("runningJobId = %s, want %s", conflict.RunningJobID, first.ID)
	}
	if conflict.Detail == "" {
		t.Fatal("conflict must explain itself")
	}
}

func TestReadonlyJobsRunInParallel(t *testing.T) {
	planner := &fakePlanner{plans: map[string]*actions.Plan{
		"backup-check": {
			Action: "backup-check", Title: "检查备份仓库 @ pg-test", Risk: plan.RiskReadonly,
			Cluster: strPtr("pg-test"), Targets: []string{"10.10.10.11"}, ConfigHash: "hash-1",
			Commands: []string{"ansible 10.10.10.11 ..."},
			Steps: []actions.Command{{
				Argv:    []string{"ansible", "10.10.10.11", "-i", "pigsty.yml", "-m", "shell", "-a", "pgbackrest check"},
				Display: "ansible 10.10.10.11 ...", Kind: "adhoc", Hosts: []string{"10.10.10.11"},
			}},
		},
	}}
	manager, _ := testManager(t, planner, 0.02)
	first, err := manager.Submit(context.Background(), SubmitRequest{Action: "backup-check", User: "admin"})
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	second, err := manager.Submit(context.Background(), SubmitRequest{Action: "backup-check", User: "admin"})
	if err != nil {
		t.Fatalf("readonly jobs must not block each other: %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		manager.Cancel(context.Background(), id)
		waitFor(t, manager, id)
	}
}

func TestSubmitRejectsBlockedPreconditions(t *testing.T) {
	planner := &fakePlanner{plans: map[string]*actions.Plan{
		"pgsql-rm-replica": {
			Action: "pgsql-rm-replica", Title: "移除副本实例", Risk: plan.RiskHigh,
			Cluster: strPtr("pg-test"),
			Preconditions: []plan.Precondition{
				plan.Err("not_primary", "10.10.10.11 是主库，不能移除"),
			},
			Steps: []actions.Command{{Argv: []string{"./pgsql-rm.yml", "-l", "10.10.10.11"}, Kind: "playbook"}},
		},
	}}
	manager, _ := testManager(t, planner, 1)
	_, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-rm-replica", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("error = %v, want BlockedError", err)
	}
	if len(blocked.Issues) != 1 || blocked.Issues[0].Path != "not_primary" {
		t.Fatalf("issues = %+v", blocked.Issues)
	}
	if _, total, _ := manager.List(JobFilter{}); total != 0 {
		t.Fatalf("rejected jobs must not be recorded, total = %d", total)
	}
}

func TestSubmitRequiresGateConfirmation(t *testing.T) {
	planner := &fakePlanner{plans: map[string]*actions.Plan{
		"pg-switchover": {
			Action: "pg-switchover", Title: "主从切换", Risk: plan.RiskHigh, Cluster: strPtr("pg-test"),
			Gate: &plan.Gate{
				ConfirmText: "10.10.10.12",
				Checkboxes:  []string{"我已确认业务可以接受一次主从切换"},
			},
			Steps: []actions.Command{{Argv: []string{"./pgsql.yml", "-l", "pg-test"}, Kind: "playbook"}},
		},
	}}
	manager, _ := testManager(t, planner, 100)
	_, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pg-switchover", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("missing confirmation error = %v", err)
	}
	_, err = manager.Submit(context.Background(), SubmitRequest{
		Action: "pg-switchover", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
		Confirm: &Confirm{Text: "10.10.10.12", Checkboxes: []string{"我已确认业务可以接受一次主从切换"}},
	})
	if err != nil {
		t.Fatalf("confirmed submit failed: %v", err)
	}
}

func TestSanitizerMasksSecrets(t *testing.T) {
	config := `---
all:
  vars:
    pg_admin_password: DBUser.DBA
    version: v4.5.0
  children:
    pg-test:
      vars:
        pg_cluster: pg-test
        pg_users:
          - { name: dbuser_app, password: App.Password }
`
	sanitizer := NewSanitizer(config)
	line := "changed: [10.10.10.11] => pg_admin_password=DBUser.DBA user=dbuser_app password=App.Password file=pigsty.yml"
	masked := sanitizer.Text(line)
	for _, secret := range []string{"DBUser.DBA", "App.Password"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("secret %q leaked: %s", secret, masked)
		}
	}
	if !strings.Contains(masked, "pigsty.yml") {
		t.Fatalf("product name must stay readable: %s", masked)
	}
	if !strings.Contains(masked, "***") {
		t.Fatalf("mask marker missing: %s", masked)
	}
}

func strPtr(v string) *string { return &v }

func TestHighRiskJobCreatesRestorePoint(t *testing.T) {
	planner := &fakePlanner{plans: map[string]*actions.Plan{
		"pgsql-rm-replica": {
			Action: "pgsql-rm-replica", Title: "移除副本实例 @ pg-test", Risk: plan.RiskHigh,
			Cluster: strPtr("pg-test"), Targets: []string{"10.10.10.12"}, ConfigHash: "hash-1",
			RestorePt: true,
			Commands:  []string{"ansible 10.10.10.11 ..."},
			Steps: []actions.Command{{
				Argv: []string{"ansible", "10.10.10.11", "-i", "pigsty.yml", "-b", "--become-user=postgres",
					"-m", "shell", "-a", "pgbackup"},
				Display: "ansible 10.10.10.11 ...", Kind: "adhoc", Hosts: []string{"10.10.10.11"},
			}},
		},
	}}
	manager, db := testManager(t, planner, 200)
	job, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-rm-replica", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if final.Status != store.JobSuccess {
		t.Fatalf("status = %s (%v)", final.Status, final.Error)
	}
	if final.RestorePoint == nil {
		t.Fatal("high risk job must record an automatic restore point")
	}
	if final.RestorePoint.Name != "console_job_"+job.ID || final.RestorePoint.Cluster != "pg-test" {
		t.Fatalf("restore point = %+v", final.RestorePoint)
	}
	if final.RestorePoint.LSN == nil {
		t.Fatalf("simulated restore point needs an LSN: %+v", final.RestorePoint)
	}
	points, err := db.RestorePoints("pg-test")
	if err != nil {
		t.Fatalf("restore points: %v", err)
	}
	if len(points) != 1 || points[0].Name != "console_job_"+job.ID {
		t.Fatalf("restore points = %+v", points)
	}
}

func TestRestorePointActionIsRecorded(t *testing.T) {
	planner := &fakePlanner{plans: map[string]*actions.Plan{
		"restore-point": {
			Action: "restore-point", Title: "创建恢复点 before_upgrade @ pg-test", Risk: plan.RiskLow,
			Cluster: strPtr("pg-test"), Targets: []string{"10.10.10.11"}, ConfigHash: "hash-1",
			Commands: []string{"ansible 10.10.10.11 ..."},
			Steps: []actions.Command{{
				Argv: []string{"ansible", "10.10.10.11", "-i", "pigsty.yml", "-b", "--become-user=postgres",
					"-m", "shell", "-a", "psql -c \"SELECT pg_create_restore_point('before_upgrade')\""},
				Display: "ansible 10.10.10.11 ...", Kind: "adhoc", Hosts: []string{"10.10.10.11"},
			}},
		},
	}}
	manager, db := testManager(t, planner, 200)
	job, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "restore-point",
		Params: map[string]any{"cluster": "pg-test", "name": "before_upgrade"}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitFor(t, manager, job.ID)
	points, err := db.RestorePoints("pg-test")
	if err != nil {
		t.Fatalf("restore points: %v", err)
	}
	if len(points) != 1 || points[0].Name != "before_upgrade" {
		t.Fatalf("restore points = %+v", points)
	}
	if points[0].JobID == nil || *points[0].JobID != job.ID {
		t.Fatalf("restore point must reference the job: %+v", points[0])
	}
	if points[0].LSN == nil {
		t.Fatalf("simulated restore point needs an LSN: %+v", points[0])
	}
}

func TestAppliedVersionIsRecorded(t *testing.T) {
	planner := &fakePlanner{}
	manager, db := testManager(t, planner, 200)
	job, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-svc", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitFor(t, manager, job.ID)
	rows, err := db.LatestApplied()
	if err != nil {
		t.Fatalf("applied: %v", err)
	}
	row, ok := rows["pg-test"]
	if !ok {
		t.Fatalf("applied rows = %+v", rows)
	}
	if row.JobID == nil || *row.JobID != job.ID || row.Commit != "hash-1" {
		t.Fatalf("applied row = %+v", row)
	}

	// A check job must not move the applied version.
	check, err := manager.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-hba", Params: map[string]any{"cluster": "pg-test"}, Check: true, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit check: %v", err)
	}
	waitFor(t, manager, check.ID)
	rows, err = db.LatestApplied()
	if err != nil {
		t.Fatalf("applied: %v", err)
	}
	if rows["pg-test"].JobID == nil || *rows["pg-test"].JobID != job.ID {
		t.Fatalf("check jobs must not update applied: %+v", rows)
	}
}

func TestReattachFinishedJobAfterRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "console.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	options := Options{
		Home: dir, DataDir: dir, Logger: logger, Store: db, Planner: &fakePlanner{},
		Simulate: true, SimulateSpeed: 400,
	}
	first := New(options)
	job, err := first.Submit(context.Background(), SubmitRequest{
		Action: "pgsql-svc", Params: map[string]any{"cluster": "pg-test"}, User: "admin",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	final := waitFor(t, first, job.ID)
	if final.Status != store.JobSuccess {
		t.Fatalf("status = %s", final.Status)
	}
	first.Shutdown()

	// A fresh console finds the job in SQLite, replays its events and closes the
	// stream immediately.
	second := New(options)
	if err := second.Recover(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	reloaded, err := second.Get(job.ID)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if reloaded.Status != store.JobSuccess {
		t.Fatalf("status after restart = %s", reloaded.Status)
	}
	history, ch, cancel, err := second.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()
	if len(history) == 0 {
		t.Fatal("history must be replayed after a restart")
	}
	if history[0].Seq != 1 {
		t.Fatalf("history starts at seq %d", history[0].Seq)
	}
	last := history[len(history)-1]
	if last.Type != EventStatus || last.Job == nil || last.Job.Status != store.JobSuccess {
		t.Fatalf("history must end with a terminal status: %+v", last)
	}
	select {
	case <-second.Done(job.ID):
	case <-time.After(time.Second):
		t.Fatal("finished job must report a closed done channel")
	}
	select {
	case <-ch:
		t.Fatal("no live events are expected for a finished job")
	case <-time.After(100 * time.Millisecond):
	}
	log, err := second.Log(job.ID)
	if err != nil {
		t.Fatalf("log after restart: %v", err)
	}
	if !strings.Contains(log, "PLAY RECAP") {
		t.Fatalf("log after restart is incomplete: %q", log)
	}
}

func TestEventBusReleasesSubscribers(t *testing.T) {
	dir := t.TempDir()
	bus, err := newEventBus(filepath.Join(dir, "events.jsonl"), 0)
	if err != nil {
		t.Fatalf("newEventBus: %v", err)
	}
	defer bus.Close()
	_, first, cancelFirst := bus.Subscribe(0)
	_, second, cancelSecond := bus.Subscribe(0)
	if count := bus.subscriberCount(); count != 2 {
		t.Fatalf("subscribers = %d, want 2", count)
	}
	bus.Append(JobEvent{Type: EventLog, Line: "hello"})
	select {
	case ev := <-first:
		if ev.Seq != 1 {
			t.Fatalf("seq = %d", ev.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the event")
	}
	cancelFirst()
	cancelSecond()
	if count := bus.subscriberCount(); count != 0 {
		t.Fatalf("disconnected subscribers leaked: %d", count)
	}
	// A cancelled subscriber keeps its already buffered events, but the channel
	// is closed so a reader can never block forever.
	deadline := time.After(time.Second)
	for closed := false; !closed; {
		select {
		case _, ok := <-second:
			closed = !ok
		case <-deadline:
			t.Fatal("cancelled subscriber channel was not closed")
		}
	}
	history := bus.Events()
	if len(history) != 1 || history[0].Line != "hello" {
		t.Fatalf("history = %+v", history)
	}
}
