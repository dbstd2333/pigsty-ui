package status

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
)

const statusConfig = `---
all:
  vars:
    version: v4.5.0
    admin_ip: 10.10.10.10
    patroni_password: Patroni.API
  children:
    infra:
      hosts:
        10.10.10.10: { infra_seq: 1 }
      vars:
        vmetrics_port: 8428
        alertmanager_port: 9059
        grafana_port: 3000
    pg-test:
      hosts:
        10.10.10.11: { pg_seq: 1, pg_role: primary }
        10.10.10.12: { pg_seq: 2, pg_role: replica }
      vars:
        pg_cluster: pg-test
    redis-test:
      hosts:
        10.10.10.13: {}
      vars:
        redis_cluster: redis-test
`

// fakeSource serves a fixed inventory to the status service.
type fakeSource struct {
	model    *inventory.Model
	settings Settings
	home     string
}

// Model returns the fixed inventory.
func (f fakeSource) Model(context.Context) (*inventory.Model, error) { return f.model, nil }

// Settings returns the configured address overrides.
func (f fakeSource) Settings() Settings { return f.settings }

// Home returns the Pigsty home directory.
func (f fakeSource) Home() string { return f.home }

// testService builds a simulated status service over the test inventory.
func testService(t *testing.T, settings Settings) *Service {
	t.Helper()
	model, err := inventory.Load([]byte(statusConfig))
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	return New(Options{
		Source:   fakeSource{model: model, settings: settings, home: t.TempDir()},
		Simulate: true,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		TTL:      time.Minute,
	})
}

func TestSimulatedNodesAreStableAndComplete(t *testing.T) {
	service := testService(t, Settings{})
	src, first := service.Nodes(context.Background())
	if !src.OK || src.URL != "simulate://victoria/nodes" {
		t.Fatalf("source = %+v", src)
	}
	if len(first) != 4 {
		t.Fatalf("nodes = %d, want 4", len(first))
	}
	for _, node := range first {
		if !node.Managed || node.Up == nil || !*node.Up {
			t.Fatalf("node %s must be up and managed: %+v", node.IP, node)
		}
		if node.CPUCores == nil || node.MemTotalBytes == nil || node.DiskTotalBytes == nil ||
			node.MemAvailableBytes == nil || node.DiskFreeBytes == nil || node.CPUUsage == nil || node.Load1 == nil {
			t.Fatalf("node %s is missing metrics: %+v", node.IP, node)
		}
	}
	// A second call must return exactly the same values.
	service2 := testService(t, Settings{})
	_, second := service2.Nodes(context.Background())
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("simulated metrics are not stable:\n%+v\n%+v", first, second)
	}
}

func TestSimulatedClusterHasOneRoleMismatch(t *testing.T) {
	service := testService(t, Settings{})
	ctx := context.Background()
	clusters := service.Clusters(ctx)
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(clusters))
	}
	cluster := clusters[0]
	if !cluster.Source.OK || cluster.Leader == nil || cluster.Paused == nil || cluster.Healthy == nil {
		t.Fatalf("cluster status is incomplete: %+v", cluster)
	}
	if len(cluster.Members) < 2 {
		t.Fatalf("members = %+v", cluster.Members)
	}
	roles := map[string]int{}
	for _, member := range cluster.Members {
		roles[member.Role]++
	}
	if roles["leader"] != 1 {
		t.Fatalf("members must have exactly one leader: %+v", cluster.Members)
	}

	drifts := service.Drift(ctx)
	mismatches := 0
	for _, drift := range drifts {
		switch drift.Kind {
		case "role_mismatch":
			mismatches++
			if drift.Fix == nil || drift.Fix.Primary == "" {
				t.Fatalf("role mismatch must suggest a fix: %+v", drift)
			}
			if drift.Message == "" {
				t.Fatalf("drift message is empty: %+v", drift)
			}
		case "missing_member", "unknown_member":
		default:
			t.Fatalf("unexpected drift kind %q", drift.Kind)
		}
	}
	if mismatches == 0 {
		t.Fatalf("drifts = %+v, want at least one role_mismatch", drifts)
	}
	// Every mismatch of a swapped pair must suggest the same primary.
	suggested := ""
	for _, drift := range drifts {
		if drift.Kind != "role_mismatch" {
			continue
		}
		if suggested == "" {
			suggested = drift.Fix.Primary
		} else if drift.Fix.Primary != suggested {
			t.Fatalf("conflicting fixes: %+v", drifts)
		}
	}
}

func TestSimulatedAlertsAndConfig(t *testing.T) {
	service := testService(t, Settings{})
	src, alerts := service.Alerts(context.Background())
	if !src.OK || len(alerts) == 0 {
		t.Fatalf("alerts = %+v (%+v)", alerts, src)
	}
	for _, alert := range alerts {
		if alert.Name == "" || alert.Severity == "" || alert.Summary == "" || alert.StartsAt.IsZero() {
			t.Fatalf("alert is incomplete: %+v", alert)
		}
	}
	configSrc, params := service.PGConfig(context.Background(), "pg-test")
	if !configSrc.OK {
		t.Fatalf("config source = %+v", configSrc)
	}
	for _, key := range []string{"shared_buffers", "work_mem", "max_connections", "wal_buffers"} {
		if params[key] == "" {
			t.Fatalf("parameter %s is missing: %+v", key, params)
		}
	}
}

func TestDerivedURLsAndOverrides(t *testing.T) {
	service := testService(t, Settings{})
	derived := service.Derived(context.Background())
	if derived.VictoriaURL != "http://10.10.10.10:8428" {
		t.Fatalf("victoria url = %s", derived.VictoriaURL)
	}
	if derived.AlertmanagerURL != "http://10.10.10.10:9059" {
		t.Fatalf("alertmanager url = %s", derived.AlertmanagerURL)
	}
	if derived.GrafanaURL != "http://10.10.10.10:3000" {
		t.Fatalf("grafana url = %s", derived.GrafanaURL)
	}
	override := testService(t, Settings{VictoriaURL: "http://10.0.0.9:9999/"})
	urls := override.URLs(context.Background())
	if urls.VictoriaURL != "http://10.0.0.9:9999" {
		t.Fatalf("override ignored: %+v", urls)
	}
	if urls.AlertmanagerURL != "http://10.10.10.10:9059" {
		t.Fatalf("derived value lost: %+v", urls)
	}
}

func TestParseSystemctlMemory(t *testing.T) {
	out := `Id=patroni.service
MemoryCurrent=125829120

Id=postgresql.service
MemoryCurrent=536870912

Id=pgbouncer.service
MemoryCurrent=[not set]

Id=vector.service
MemoryCurrent=18446744073709551615

Id=etcd.service
MemoryCurrent=infinite
`
	parsed := parseSystemctlMemory(out)
	patroni := parsed["patroni.service"]
	if patroni == nil || *patroni != 125829120 {
		t.Fatalf("patroni = %v", patroni)
	}
	postgres := parsed["postgresql.service"]
	if postgres == nil || *postgres != 536870912 {
		t.Fatalf("postgresql = %v", postgres)
	}
	for _, unit := range []string{"pgbouncer.service", "vector.service", "etcd.service"} {
		if parsed[unit] != nil {
			t.Fatalf("%s must stay unknown, got %v", unit, *parsed[unit])
		}
	}
}

func TestSimulatedMemoryIsStable(t *testing.T) {
	service := testService(t, Settings{})
	src, first := service.Memory(context.Background(), "10.10.10.11", false)
	if !src.OK {
		t.Fatalf("source = %+v", src)
	}
	if len(first) == 0 {
		t.Fatal("no services returned")
	}
	postgres := int64(0)
	for _, item := range first {
		if item.Name == "postgresql" {
			if item.Bytes == nil {
				t.Fatal("postgresql memory must be present in simulate mode")
			}
			postgres = *item.Bytes
		}
	}
	if postgres == 0 {
		t.Fatal("postgresql memory missing")
	}
	_, second := service.Memory(context.Background(), "10.10.10.11", false)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("simulated memory is not stable")
	}
}

func TestParseVMLabels(t *testing.T) {
	sample := Series{Labels: map[string]string{"ip": "10.10.10.11", "instance": "10.10.10.11:9100"}}
	if sample.InstanceIP() != "10.10.10.11" {
		t.Fatalf("ip label = %s", sample.InstanceIP())
	}
	fallback := Series{Labels: map[string]string{"instance": "10.10.10.12:9100"}}
	if fallback.InstanceIP() != "10.10.10.12" {
		t.Fatalf("instance label = %s", fallback.InstanceIP())
	}
	samples := []Series{
		{Labels: map[string]string{"ip": "10.10.10.11", "mountpoint": "/"}, Value: 100},
		{Labels: map[string]string{"ip": "10.10.10.11", "mountpoint": "/data"}, Value: 200},
	}
	if v, ok := pickDisk(samples, "10.10.10.11"); !ok || v != 200 {
		t.Fatalf("pickDisk = %v %v, want the data mount", v, ok)
	}
}

func TestParseLagValues(t *testing.T) {
	if v := parseLag([]byte(`0`)); v == nil || *v != 0 {
		t.Fatalf("numeric lag = %v", v)
	}
	if v := parseLag([]byte(`"1048576"`)); v == nil || *v != 1048576 {
		t.Fatalf("string lag = %v", v)
	}
	if v := parseLag([]byte(`"unknown"`)); v != nil {
		t.Fatalf("unknown lag must be nil, got %v", *v)
	}
	if v := parseLag(nil); v != nil {
		t.Fatalf("missing lag must be nil, got %v", *v)
	}
}
