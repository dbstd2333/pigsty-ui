package actions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// testConfig is a small but complete Pigsty inventory for the planner tests.
const testConfig = `---
all:
  vars:
    version: v4.5.0
    admin_ip: 10.10.10.10
    pg_dbsu: postgres
    pgbackrest_repo:
      local: { path: /pg/backup, retention_full_type: count, retention_full: 2 }
      minio: { type: s3, s3_endpoint: sss.pigsty, cipher_type: aes-256-cbc }
  children:
    infra:
      hosts:
        10.10.10.10: { infra_seq: 1 }
      vars:
        vmetrics_port: 8428
    pg-test:
      hosts:
        10.10.10.11: { pg_seq: 1, pg_role: primary }
        10.10.10.12: { pg_seq: 2, pg_role: replica }
      vars:
        pg_cluster: pg-test
        pgbackrest_method: minio
        pg_users:
          - { name: dbuser_app, password: App.Password }
          - { name: dbuser_gone, password: App.Password, state: absent }
        pg_databases:
          - { name: app }
          - { name: legacy, state: absent }
    redis-test:
      hosts:
        10.10.10.10: {}
      vars:
        redis_cluster: redis-test
    etcd:
      hosts:
        10.10.10.11: { etcd_seq: 1 }
      vars:
        etcd_cluster: etcd
`

// fakeFacts is a static implementation of the external state of a plan.
type fakeFacts struct {
	members map[string][]Member
	windows map[string]Window
	managed map[string]bool
}

// PatroniMembers returns the configured members of a cluster.
func (f *fakeFacts) PatroniMembers(_ context.Context, cluster string) ([]Member, bool) {
	members, ok := f.members[cluster]
	return members, ok
}

// BackupWindow returns the configured recoverable window of a cluster.
func (f *fakeFacts) BackupWindow(_ context.Context, cluster string) (Window, bool) {
	window, ok := f.windows[cluster]
	return window, ok
}

// ManagedNodes returns the hosts that report metrics.
func (f *fakeFacts) ManagedNodes(context.Context) map[string]bool { return f.managed }

// testPlanner builds a planner over the test inventory.
func testPlanner(t *testing.T, facts Facts) *Planner {
	t.Helper()
	p, err := NewPlanner(Options{
		Facts:    facts,
		Settings: stubSettings{},
		Snapshot: func(context.Context) (Snapshot, error) {
			return Snapshot{Text: testConfig, Hash: "hash-1", Commit: "commit-1"}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	return p
}

type stubSettings struct{}

func (stubSettings) BackupMaxAgeHours() int { return 26 }

// testFacts returns the standard external state of the planner tests.
func testFacts() *fakeFacts {
	return &fakeFacts{
		members: map[string][]Member{
			"pg-test": {
				{Name: "pg-test-1", Host: "10.10.10.11", Role: "leader", State: "running"},
				{Name: "pg-test-2", Host: "10.10.10.12", Role: "replica", State: "streaming"},
			},
		},
		windows: map[string]Window{
			"pg-test": {
				Start:        time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC),
				End:          time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
				LastBackupAt: ptrTime(time.Date(2026, 10, 16, 1, 3, 0, 0, time.UTC)),
			},
		},
		managed: map[string]bool{"10.10.10.10": true, "10.10.10.11": true, "10.10.10.12": true},
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestCatalogLoadsMVP(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{
		"pgsql-create", "pgsql-user", "pgsql-db", "pgsql-hba", "pgsql-svc", "pgsql-ext", "pgsql-crontab",
		"pgsql-add-replica", "pgsql-rm-replica", "patroni-param", "pg-restart", "pg-switchover",
		"node-add", "redis-create", "backup-run", "backup-check", "restore-point",
		"pitr-clone", "pitr-inplace", "pitr-promote",
	}
	got := map[string]bool{}
	for _, def := range catalog.List() {
		got[def.ID] = true
		for _, param := range def.Params {
			if param.Enum == nil {
				t.Fatalf("%s.%s: enum must not be nil", def.ID, param.Name)
			}
		}
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("catalog is missing %s", id)
		}
	}
	if len(want) != len(catalog.List()) {
		t.Fatalf("catalog has %d actions, want %d", len(catalog.List()), len(want))
	}
}

func TestPlanRendersWhitelistedCommands(t *testing.T) {
	planner := testPlanner(t, testFacts())

	user, err := planner.Plan(context.Background(), "pgsql-user",
		map[string]any{"cluster": "pg-test", "user": "dbuser_app"}, false)
	if err != nil {
		t.Fatalf("plan pgsql-user: %v", err)
	}
	if len(user.Commands) != 1 || user.Commands[0] != "./pgsql-user.yml -l pg-test -e username=dbuser_app" {
		t.Fatalf("unexpected command: %v", user.Commands)
	}
	if user.Gate != nil {
		t.Fatalf("low risk action must not have a gate: %+v", user.Gate)
	}
	if len(user.Targets) != 2 || user.Targets[0] != "10.10.10.11" {
		t.Fatalf("targets = %v", user.Targets)
	}

	check, err := planner.Plan(context.Background(), "pgsql-user",
		map[string]any{"cluster": "pg-test", "user": "dbuser_app"}, true)
	if err != nil {
		t.Fatalf("plan pgsql-user check: %v", err)
	}
	if !strings.Contains(check.Commands[0], "--check") {
		t.Fatalf("check plan misses --check: %v", check.Commands)
	}

	tags, err := planner.Plan(context.Background(), "pgsql-hba", map[string]any{"cluster": "pg-test"}, false)
	if err != nil {
		t.Fatalf("plan pgsql-hba: %v", err)
	}
	want := "./pgsql.yml -l pg-test -t pg_hba,pg_reload,pgbouncer_hba,pgbouncer_reload"
	if tags.Commands[0] != want {
		t.Fatalf("pgsql-hba command = %q, want %q", tags.Commands[0], want)
	}

	adhoc, err := planner.Plan(context.Background(), "backup-run",
		map[string]any{"cluster": "pg-test", "mode": "full"}, false)
	if err != nil {
		t.Fatalf("plan backup-run: %v", err)
	}
	wantAdhoc := "ansible 10.10.10.11 -i pigsty.yml -b --become-user=postgres -m shell -a '/pg/bin/pg-backup full'"
	if adhoc.Commands[0] != wantAdhoc {
		t.Fatalf("backup-run command = %q, want %q", adhoc.Commands[0], wantAdhoc)
	}

	// 10.10.10.11 is also an etcd node, so the member name must come from the
	// pg-test group and not from the first group listing that host.
	restart, err := planner.Plan(context.Background(), "pg-restart",
		map[string]any{"cluster": "pg-test", "member": "10.10.10.12"}, false)
	if err != nil {
		t.Fatalf("plan pg-restart: %v", err)
	}
	if !strings.Contains(restart.Commands[0], "restart pg-test pg-test-2 --force") {
		t.Fatalf("restart command = %q", restart.Commands[0])
	}
}

func TestPlanRejectsBadParameters(t *testing.T) {
	planner := testPlanner(t, testFacts())
	cases := []struct {
		name   string
		action string
		params map[string]any
		path   string
	}{
		{"injection in cluster name", "pgsql-user",
			map[string]any{"cluster": "pg-test; rm -rf /", "user": "dbuser_app"}, "cluster"},
		{"unknown cluster", "pgsql-user", map[string]any{"cluster": "pg-nope", "user": "dbuser_app"}, "cluster"},
		{"unknown user", "pgsql-user", map[string]any{"cluster": "pg-test", "user": "dbuser_none"}, "user"},
		{"user defined in another cluster", "pgsql-user",
			map[string]any{"cluster": "pg-test", "user": "dbuser'x"}, "user"},
		{"bad ip", "pgsql-add-replica",
			map[string]any{"cluster": "pg-test", "ips": []any{"10.10.10.99"}}, "ips"},
		{"not an ip", "pgsql-add-replica",
			map[string]any{"cluster": "pg-test", "ips": []any{"8.8.8.8; reboot"}}, "ips"},
		{"unknown member", "pg-restart",
			map[string]any{"cluster": "pg-test", "member": "10.10.10.13"}, "member"},
		{"unknown param", "pgsql-svc",
			map[string]any{"cluster": "pg-test", "extra": "x"}, "extra"},
		{"unknown enum", "backup-run",
			map[string]any{"cluster": "pg-test", "mode": "super"}, "mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planner.Plan(context.Background(), tc.action, tc.params, false)
			var paramErr *ParamError
			if !errors.As(err, &paramErr) {
				t.Fatalf("error = %v, want ParamError", err)
			}
			found := false
			for _, issue := range paramErr.Issues {
				if issue.Path == tc.path {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %+v, want path %s", paramErr.Issues, tc.path)
			}
		})
	}
	if _, err := planner.Plan(context.Background(), "no-such-action", nil, false); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("unknown action error = %v", err)
	}
	if _, err := planner.Plan(context.Background(), "pgsql-ext", map[string]any{"cluster": "pg-test"}, true); err == nil {
		t.Fatal("check mode must be rejected for actions without --check support")
	}
}

func TestPlanRefusesPrimaryRemoval(t *testing.T) {
	planner := testPlanner(t, testFacts())
	computed, err := planner.Plan(context.Background(), "pgsql-rm-replica",
		map[string]any{"cluster": "pg-test", "ips": []any{"10.10.10.11"}}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if computed.Risk != plan.RiskHigh {
		t.Fatalf("risk = %s, want high", computed.Risk)
	}
	if computed.Blocked() == "" {
		t.Fatalf("removing the primary must be blocked: %+v", computed.Preconditions)
	}
	if computed.Gate == nil || computed.Gate.ConfirmText != "10.10.10.11" {
		t.Fatalf("gate = %+v", computed.Gate)
	}

	replica, err := planner.Plan(context.Background(), "pgsql-rm-replica",
		map[string]any{"cluster": "pg-test", "ips": []any{"10.10.10.12"}}, false)
	if err != nil {
		t.Fatalf("plan replica removal: %v", err)
	}
	if replica.Blocked() != "" {
		t.Fatalf("replica removal must pass: %s", replica.Blocked())
	}
	if replica.Commands[0] != "./pgsql-rm.yml -l 10.10.10.12" {
		t.Fatalf("commands = %v", replica.Commands)
	}
	if replica.Commands[1] != "./pgsql.yml -l pg-test -t pg_service" {
		t.Fatalf("commands = %v", replica.Commands)
	}
	if replica.RestorePt != true {
		t.Fatal("high risk jobs need an automatic restore point")
	}
}

func TestPlanUpgradesRiskForDestructiveUser(t *testing.T) {
	planner := testPlanner(t, testFacts())
	computed, err := planner.Plan(context.Background(), "pgsql-user",
		map[string]any{"cluster": "pg-test", "user": "dbuser_gone"}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if computed.Risk != plan.RiskHigh {
		t.Fatalf("risk = %s, want high for state=absent", computed.Risk)
	}
	if computed.Gate == nil {
		t.Fatal("destructive user change needs a gate")
	}
	if computed.Gate.LastBackupAt == nil || computed.Gate.BackupMaxAgeHours != 26 {
		t.Fatalf("gate = %+v", computed.Gate)
	}
}

func TestPlanPitrInplaceGateAndPayload(t *testing.T) {
	planner := testPlanner(t, testFacts())
	target := map[string]any{"type": "time", "time": "2026-10-15T10:30:00+08:00"}
	computed, err := planner.Plan(context.Background(), "pitr-inplace",
		map[string]any{"cluster": "pg-test", "target": target, "action": "pause", "backup": true}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if computed.Risk != plan.RiskCritical {
		t.Fatalf("risk = %s, want critical", computed.Risk)
	}
	if computed.Gate == nil || computed.Gate.ConfirmText != "pg-test" {
		t.Fatalf("gate = %+v", computed.Gate)
	}
	if len(computed.Gate.Checkboxes) == 0 ||
		!strings.Contains(strings.Join(computed.Gate.Checkboxes, "|"), "我已确认业务已停写或已通知相关方") {
		t.Fatalf("checkboxes = %v", computed.Gate.Checkboxes)
	}
	command := computed.Commands[0]
	if !strings.HasPrefix(command, "./pgsql-pitr.yml -l pg-test -e '{\"pg_pitr\":") {
		t.Fatalf("command = %q", command)
	}
	for _, want := range []string{`"time":"2026-10-15 02:30:00+00"`, `"backup":true`, `"action":"pause"`} {
		if !strings.Contains(command, want) {
			t.Fatalf("command %q misses %s", command, want)
		}
	}
	if computed.Blocked() != "" {
		t.Fatalf("blocked: %s", computed.Blocked())
	}

	// A target outside the recoverable window is an error precondition.
	late, err := planner.Plan(context.Background(), "pitr-inplace", map[string]any{
		"cluster": "pg-test", "target": map[string]any{"type": "time", "time": "2026-10-20 00:00:00+00"},
		"action": "pause"}, false)
	if err != nil {
		t.Fatalf("plan late: %v", err)
	}
	if late.Blocked() == "" {
		t.Fatalf("target outside the window must be blocked: %+v", late.Preconditions)
	}
}

func TestPlanClusterAbsentForCreate(t *testing.T) {
	facts := testFacts()
	planner := testPlanner(t, facts)
	computed, err := planner.Plan(context.Background(), "pgsql-create", map[string]any{"cluster": "pg-test"}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if computed.Blocked() == "" {
		t.Fatalf("existing cluster must not be re-initialised: %+v", computed.Preconditions)
	}
	delete(facts.members, "pg-test")
	planner = testPlanner(t, facts)
	computed, err = planner.Plan(context.Background(), "pgsql-create", map[string]any{"cluster": "pg-test"}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if computed.Blocked() != "" {
		t.Fatalf("empty cluster must pass: %s", computed.Blocked())
	}
}

func TestPlanNodeAddWarnsUnknownHost(t *testing.T) {
	planner := testPlanner(t, testFacts())
	computed, err := planner.Plan(context.Background(), "node-add", map[string]any{"ips": []any{"10.10.10.15"}}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if computed.Commands[0] != "./node.yml -l 10.10.10.15" {
		t.Fatalf("commands = %v", computed.Commands)
	}
	found := false
	for _, cond := range computed.Preconditions {
		if cond.ID == "host_in_inventory" && cond.Level == "warning" && !cond.OK {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning precondition: %+v", computed.Preconditions)
	}
	if computed.Gate != nil {
		t.Fatalf("medium risk action must not have a gate: %+v", computed.Gate)
	}
}

func TestEveryRenderedCommandIsGuarded(t *testing.T) {
	planner := testPlanner(t, testFacts())
	ctx := context.Background()
	cases := []struct {
		action string
		params map[string]any
	}{
		{"pgsql-create", map[string]any{"cluster": "pg-test"}},
		{"pgsql-user", map[string]any{"cluster": "pg-test", "user": "dbuser_app"}},
		{"pgsql-db", map[string]any{"cluster": "pg-test", "db": "app"}},
		{"pgsql-hba", map[string]any{"cluster": "pg-test"}},
		{"pgsql-svc", map[string]any{"cluster": "pg-test"}},
		{"pgsql-ext", map[string]any{"cluster": "pg-test"}},
		{"pgsql-crontab", map[string]any{"cluster": "pg-test"}},
		{"pgsql-add-replica", map[string]any{"cluster": "pg-test", "ips": []any{"10.10.10.12"}}},
		{"pgsql-rm-replica", map[string]any{"cluster": "pg-test", "ips": []any{"10.10.10.12"}}},
		{"patroni-param", map[string]any{"cluster": "pg-test", "settings": map[string]any{"max_connections": 200}}},
		{"pg-restart", map[string]any{"cluster": "pg-test", "member": "10.10.10.12"}},
		{"pg-switchover", map[string]any{"cluster": "pg-test", "leader": "10.10.10.11", "candidate": "10.10.10.12"}},
		{"node-add", map[string]any{"ips": []any{"10.10.10.11"}}},
		{"redis-create", map[string]any{"cluster": "redis-test"}},
		{"backup-run", map[string]any{"cluster": "pg-test", "mode": "incr"}},
		{"backup-check", map[string]any{"cluster": "pg-test"}},
		{"restore-point", map[string]any{"cluster": "pg-test", "name": "before_upgrade"}},
		{"pitr-clone", map[string]any{"cluster": "pg-test", "source": "pg-test",
			"target": map[string]any{"type": "name", "name": "before_upgrade"}, "action": "pause"}},
		{"pitr-inplace", map[string]any{"cluster": "pg-test",
			"target": map[string]any{"type": "latest"}, "action": "pause"}},
		{"pitr-promote", map[string]any{"cluster": "pg-test"}},
	}
	for _, tc := range cases {
		computed, err := planner.Plan(ctx, tc.action, tc.params, false)
		if err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		if len(computed.Steps) == 0 {
			t.Fatalf("%s rendered no step", tc.action)
		}
		for _, step := range computed.Steps {
			if err := AssertArgv(step.Argv); err != nil {
				t.Fatalf("%s: %v (argv %v)", tc.action, err, step.Argv)
			}
			if strings.Contains(step.Display, "all") && step.Kind == "playbook" {
				t.Fatalf("%s renders a full inventory limit: %s", tc.action, step.Display)
			}
		}
	}
}

func TestAssertArgvRejectsUnlimitedCommands(t *testing.T) {
	cases := [][]string{
		{"./pgsql.yml"},
		{"./pgsql.yml", "-l"},
		{"./pgsql.yml", "-l", ""},
		{"./pgsql.yml", "-l", "all"},
		{"ansible", "all", "-i", "pigsty.yml", "-m", "shell", "-a", "uptime"},
		{"ansible", "-i", "pigsty.yml", "-m", "shell", "-a", "uptime"},
		{"rm", "-rf", "/"},
	}
	for _, argv := range cases {
		if err := AssertArgv(argv); err == nil {
			t.Fatalf("AssertArgv(%v) = nil, want error", argv)
		}
	}
	if err := AssertArgv([]string{"./pgsql.yml", "-l", "pg-test"}); err != nil {
		t.Fatalf("valid playbook command rejected: %v", err)
	}
	if err := AssertArgv([]string{"ansible", "10.10.10.11", "-i", "pigsty.yml", "-m", "shell", "-a", "uptime"}); err != nil {
		t.Fatalf("valid ad-hoc command rejected: %v", err)
	}
}

func TestPlanLocalRepoBlocksClone(t *testing.T) {
	facts := testFacts()
	planner := testPlanner(t, facts)
	computed, err := planner.Plan(context.Background(), "pitr-clone", map[string]any{
		"cluster": "pg-test", "source": "pg-test",
		"target": map[string]any{"type": "latest"}, "action": "pause",
	}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if computed.Blocked() == "" {
		t.Fatalf("cloning a cluster onto itself must be blocked: %+v", computed.Preconditions)
	}
}
