package inventory

import (
	"errors"
	"strings"
	"testing"
)

const sampleInventory = `---
all:
  children:
    pg-a:
      hosts:
        10.10.10.11: { pg_seq: 1, pg_role: primary }
        10.10.10.12: { pg_seq: 2, pg_role: replica }
      vars:
        pg_cluster: pg-a
        conflict_key: from-pg-a
        group_only: yes
    pg-b:
      hosts:
        10.10.10.11: {}
      vars:
        pg_cluster: pg-b
        conflict_key: from-pg-b
        pg_parameters:
          max_connections: 200
    infra:
      hosts:
        10.10.10.10: { infra_seq: 1 }
      vars:
        repo_enabled: false
    etcd:
      hosts:
        10.10.10.10: { etcd_seq: 1 }
      vars:
        etcd_cluster: etcd
  vars:
    version: v4.5.0
    admin_ip: ${admin_ip}
    conflict_key: from-global
    global_only: yes
`

// SensitiveForTest mimics the schema sensitive check in tests.
func SensitiveForTest(key string) bool {
	return strings.Contains(key, "password") || strings.Contains(key, "secret")
}

// loadSample parses the shared test inventory.
func loadSample(t *testing.T) *Model {
	t.Helper()
	m, err := Load([]byte(sampleInventory))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

// findVar returns one effective value by key.
func findVar(t *testing.T, vars []Var, key string) Var {
	t.Helper()
	for _, v := range vars {
		if v.Key == key {
			return v
		}
	}
	t.Fatalf("key %s not found in %d effective values", key, len(vars))
	return Var{}
}

func TestEffectiveSourcesAndPrecedence(t *testing.T) {
	m := loadSample(t)
	defaults := map[string]any{
		"pg_cluster":        "default-cluster",
		"default_only":      "from-default",
		"conflict_key":      "from-default",
		"pg_admin_password": "DBUser.DBA",
	}
	vars, err := m.Effective("10.10.10.11", defaults, SensitiveForTest)
	if err != nil {
		t.Fatalf("effective: %v", err)
	}

	// The host belongs to pg-a and pg-b; ansible merges group vars in
	// (depth, priority, name) order, so the later group name wins.
	if v := findVar(t, vars, "pg_cluster"); v.Value != "pg-b" || v.Source != "group" || v.SourceGroup != "pg-b" {
		t.Errorf("pg_cluster = %+v, want group pg-b", v)
	}
	if v := findVar(t, vars, "conflict_key"); v.Value != "from-pg-b" || v.SourceGroup != "pg-b" {
		t.Errorf("conflict_key = %+v, want group pg-b", v)
	}
	if v := findVar(t, vars, "global_only"); v.Value != "yes" || v.Source != "global" {
		t.Errorf("global_only = %+v, want global", v)
	}
	if v := findVar(t, vars, "default_only"); v.Value != "from-default" || v.Source != "default" {
		t.Errorf("default_only = %+v, want default", v)
	}
	if v := findVar(t, vars, "admin_ip"); v.Value != "${admin_ip}" || v.Source != "global" {
		t.Errorf("admin_ip = %+v, want the literal placeholder", v)
	}
	if v := findVar(t, vars, "pg_admin_password"); !v.Sensitive {
		t.Errorf("pg_admin_password should be marked sensitive")
	}
	if v := findVar(t, vars, "version"); v.Sensitive {
		t.Errorf("version should not be marked sensitive")
	}
	if v := findVar(t, vars, "pg_parameters"); v.Source != "group" || v.SourceGroup != "pg-b" {
		t.Errorf("pg_parameters = %+v, want group pg-b", v)
	}
}

func TestEffectiveHostVarsWin(t *testing.T) {
	m := loadSample(t)
	vars, err := m.Effective("10.10.10.11", map[string]any{"pg_cluster": "default"}, func(string) bool { return false })
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if v := findVar(t, vars, "pg_seq"); v.Source != "host" || v.Value != 1 {
		t.Errorf("pg_seq = %+v, want host value 1", v)
	}
	if v := findVar(t, vars, "pg_role"); v.Source != "host" || v.Value != "primary" {
		t.Errorf("pg_role = %+v, want host value primary", v)
	}
}

func TestEffectiveFromHostVars(t *testing.T) {
	m := loadSample(t)
	hostVars := map[string]any{
		"pg_cluster":   "pg-b", // group level
		"pg_seq":       1,      // host level
		"conflict_key": "from-pg-b",
		"global_only":  "yes", // global level, as resolved by ansible
		"extra_var":    "only-in-ansible",
	}
	defaults := map[string]any{"pg_cluster": "default", "default_only": "from-default"}
	vars, err := m.EffectiveFromHostVars("10.10.10.11", hostVars, defaults, func(string) bool { return false })
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if v := findVar(t, vars, "pg_seq"); v.Source != "host" {
		t.Errorf("pg_seq source = %q, want host", v.Source)
	}
	if v := findVar(t, vars, "pg_cluster"); v.Source != "group" || v.SourceGroup != "pg-b" {
		t.Errorf("pg_cluster = %+v, want group pg-b", v)
	}
	if v := findVar(t, vars, "conflict_key"); v.Source != "group" || v.SourceGroup != "pg-b" {
		t.Errorf("conflict_key = %+v, want group pg-b", v)
	}
	if v := findVar(t, vars, "global_only"); v.Source != "global" || v.Value != "yes" {
		t.Errorf("global_only = %+v, want the global value", v)
	}
	if v := findVar(t, vars, "default_only"); v.Source != "default" {
		t.Errorf("default_only = %+v, want default", v)
	}
	if v := findVar(t, vars, "extra_var"); v.Value != "only-in-ansible" {
		t.Errorf("extra_var = %+v, want the ansible value", v)
	}
}

func TestEffectiveUnknownHost(t *testing.T) {
	m := loadSample(t)
	if _, err := m.Effective("10.9.9.9", nil, nil); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("err = %v, want ErrHostNotFound", err)
	}
}

func TestListsAndModules(t *testing.T) {
	m := loadSample(t)
	hosts, groups := m.Lists()
	var ips []string
	for _, h := range hosts {
		ips = append(ips, h.IP)
	}
	want := []string{"10.10.10.10", "10.10.10.11", "10.10.10.12"}
	if strings.Join(ips, ",") != strings.Join(want, ",") {
		t.Errorf("hosts = %v, want %v (IP order)", ips, want)
	}
	modules := map[string]string{}
	for _, g := range groups {
		if g.Module != nil {
			modules[g.Name] = *g.Module
		}
	}
	for name, wantModule := range map[string]string{
		"pg-a": "PGSQL", "pg-b": "PGSQL", "infra": "INFRA", "etcd": "ETCD",
	} {
		if modules[name] != wantModule {
			t.Errorf("module of %s = %q, want %q", name, modules[name], wantModule)
		}
	}
	if hosts[1].Groups[0] != "pg-a" || len(hosts[1].Groups) != 2 {
		t.Errorf("groups of 10.10.10.11 = %v, want [pg-a pg-b]", hosts[1].Groups)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load([]byte("all: [1, 2\n")); err == nil {
		t.Fatal("expected a YAML error")
	} else {
		var le *LoadError
		if !errors.As(err, &le) {
			t.Fatalf("err = %T, want *LoadError", err)
		}
		if le.Source != "yaml" || le.Line == 0 {
			t.Errorf("load error = %+v, want source yaml with a line number", le)
		}
	}
	if _, err := Load([]byte("all:\n  vars: {}\n")); err == nil {
		t.Fatal("expected a structural error")
	} else {
		var le *LoadError
		if !errors.As(err, &le) || le.Source != "inventory" {
			t.Fatalf("err = %v, want an inventory source LoadError", err)
		}
	}
}
