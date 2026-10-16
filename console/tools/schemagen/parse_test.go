package main

import (
	"os"
	"path/filepath"
	"testing"
)

const fixture = `---
#-----------------------------------------------------------------
# PG_IDENTITY
#-----------------------------------------------------------------
pg_mode: pgsql          #CLUSTER  # pgsql cluster mode: pgsql,citus
# pg_cluster:           #CLUSTER  # pgsql cluster name, required identity parameter
#pg_role: replica      #INSTANCE # pgsql role, required, could be primary,replica,offline
pg_port: 5432                     # postgres listen port, 5432 by default
pg_libs: 'pg_stat_statements, auto_explain'  # preloaded libraries
pg_note: "a # b"                  # comment character inside quotes
#nodename:           # [INSTANCE] # node instance identity, use hostname if missing
node_cluster: nodes   # [CLUSTER] # node cluster identity
#secret_path: /x       # [OPTIONAL] # optional commented key

#-----------------------------------------------------------------
# PG_BOOTSTRAP (Reference)
#-----------------------------------------------------------------
pg_data: /pg/data                 # postgres data directory

#-----------------------------------------------------------------
# PG_PROVISION
#-----------------------------------------------------------------
pg_packages:                      # pg packages to be installed, alias can be used
  - pgsql-main pgsql-common
pg_rto_plan:  # [ttl, loop, retry, start, margin]
  fast: [ 20  ,5 ]
pg_lazy:
pg_disabled: false                # disable this feature?
pg_users: []                      # postgres business users
`

// paramsOf returns the parsed params indexed by key.
func paramsOf(t *testing.T) map[string]param {
	t.Helper()
	out := map[string]param{}
	for _, p := range parseRoleDefaults("pgsql", []byte(fixture)) {
		out[p.Key] = p
	}
	return out
}

func TestParseRoleDefaults(t *testing.T) {
	params := paramsOf(t)

	cases := []struct {
		key     string
		section string
		level   string
		typ     string
		def     any
		hasDef  bool
		desc    string
	}{
		{"pg_mode", "PG_IDENTITY", "CLUSTER", "string", "pgsql", true, "pgsql cluster mode: pgsql,citus"},
		// Commented keys without an example value have no inferable type;
		// overrides.yaml fills those in (pg_cluster is a string).
		{"pg_cluster", "PG_IDENTITY", "CLUSTER", "any", nil, false, "pgsql cluster name, required identity parameter"},
		{"pg_role", "PG_IDENTITY", "INSTANCE", "string", nil, false, "pgsql role, required, could be primary,replica,offline"},
		{"pg_port", "PG_IDENTITY", "", "integer", 5432, true, "postgres listen port, 5432 by default"},
		{"pg_libs", "PG_IDENTITY", "", "string", "pg_stat_statements, auto_explain", true, "preloaded libraries"},
		{"pg_note", "PG_IDENTITY", "", "string", "a # b", true, "comment character inside quotes"},
		{"nodename", "PG_IDENTITY", "INSTANCE", "any", nil, false, "node instance identity, use hostname if missing"},
		{"node_cluster", "PG_IDENTITY", "CLUSTER", "string", "nodes", true, "node cluster identity"},
		// A commented key with an example value keeps the example's type.
		{"secret_path", "PG_IDENTITY", "", "string", nil, false, "optional commented key"},
		{"pg_data", "PG_BOOTSTRAP", "", "string", "/pg/data", true, "postgres data directory"},
		{"pg_packages", "PG_PROVISION", "", "array", []any{"pgsql-main pgsql-common"}, true, "pg packages to be installed, alias can be used"},
		{"pg_lazy", "PG_PROVISION", "", "any", nil, false, ""},
		{"pg_disabled", "PG_PROVISION", "", "boolean", false, true, "disable this feature?"},
		{"pg_users", "PG_PROVISION", "", "array", []any{}, true, "postgres business users"},
	}
	for _, c := range cases {
		p, ok := params[c.key]
		if !ok {
			t.Errorf("%s was not parsed", c.key)
			continue
		}
		if p.Section != c.section {
			t.Errorf("%s section = %q, want %q", c.key, p.Section, c.section)
		}
		if p.Level != c.level {
			t.Errorf("%s level = %q, want %q", c.key, p.Level, c.level)
		}
		if p.Type != c.typ {
			t.Errorf("%s type = %q, want %q", c.key, p.Type, c.typ)
		}
		if p.HasDefault != c.hasDef {
			t.Errorf("%s hasDefault = %v, want %v", c.key, p.HasDefault, c.hasDef)
		}
		if p.Desc != c.desc {
			t.Errorf("%s desc = %q, want %q", c.key, p.Desc, c.desc)
		}
		if c.hasDef && !equalValue(p.Default, c.def) {
			t.Errorf("%s default = %#v, want %#v", c.key, p.Default, c.def)
		}
	}
	if !params["pg_data"].Referenced {
		t.Errorf("pg_data should be marked as a reference definition")
	}
	if params["pg_mode"].Referenced {
		t.Errorf("pg_mode must not be marked as a reference definition")
	}
}

// equalValue compares parsed YAML values, tolerating numeric forms.
func equalValue(got, want any) bool {
	switch w := want.(type) {
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !equalValue(g[i], w[i]) {
				return false
			}
		}
		return true
	case int:
		switch g := got.(type) {
		case int:
			return g == w
		case float64:
			return g == float64(w)
		}
		return false
	default:
		return got == want
	}
}

func TestParseBlockValues(t *testing.T) {
	params := paramsOf(t)
	plan := params["pg_rto_plan"]
	if plan.Type != "object" {
		t.Fatalf("pg_rto_plan type = %q, want object", plan.Type)
	}
	if plan.Desc != "[ttl, loop, retry, start, margin]" {
		t.Errorf("pg_rto_plan desc = %q", plan.Desc)
	}
	m, ok := plan.Default.(map[string]any)
	if !ok {
		t.Fatalf("pg_rto_plan default = %#v, want a map", plan.Default)
	}
	if m["fast"] == nil {
		t.Errorf("pg_rto_plan default lost its fast key")
	}
}

// writeRole creates roles/<role>/defaults/main.yml inside a temp Pigsty home.
func writeRole(t *testing.T, home, role, body string) {
	t.Helper()
	dir := filepath.Join(home, "roles", role, "defaults")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write role defaults: %v", err)
	}
}

func TestReadRolesOwnerAndVersion(t *testing.T) {
	home := t.TempDir()
	writeRole(t, home, "cache", `---
#-----------------------------------------------------------------
# CACHE
#-----------------------------------------------------------------
cache_pkg_dir: dist

#-----------------------------------------------------------------
# META (Reference)
#-----------------------------------------------------------------
version: v4.5.0                   # pigsty version string
`)
	writeRole(t, home, "infra", `---
#-----------------------------------------------------------------
# INFRA_META
#-----------------------------------------------------------------
version: v4.5.0                   # pigsty version string
admin_ip: 10.10.10.10             # admin node ip address
`)
	writeRole(t, home, "pg_remove", `---
#-----------------------------------------------------------------
# PG_REMOVE
#-----------------------------------------------------------------
pg_safeguard: false               # stop pg_remove running if enabled

#-----------------------------------------------------------------
# PG_BOOTSTRAP (Reference)
#-----------------------------------------------------------------
pg_data: /pg/data                 # postgres data directory
`)
	writeRole(t, home, "pgsql", `---
#-----------------------------------------------------------------
# PG_BOOTSTRAP
#-----------------------------------------------------------------
pg_data: /pg/data                 # postgres data directory
pg_mode: pgsql          #CLUSTER  # pgsql cluster mode
`)
	writeRole(t, home, "node", `---
#-----------------------------------------------------------------
# NODE_TUNE
#-----------------------------------------------------------------
node_tune: oltp                   # node tuned profile
`)

	params, version, err := readRoles(home)
	if err != nil {
		t.Fatalf("readRoles: %v", err)
	}
	if version != "v4.5.0" {
		t.Errorf("version = %q, want v4.5.0", version)
	}
	byKey := map[string]param{}
	for _, p := range params {
		byKey[p.Key] = p
	}
	if got := byKey["version"].Role; got != "infra" {
		t.Errorf("version owner = %q, want infra (reference blocks lose)", got)
	}
	if got := byKey["pg_data"].Role; got != "pgsql" {
		t.Errorf("pg_data owner = %q, want pgsql", got)
	}
	if got := byKey["pg_safeguard"].Role; got != "pg_remove" {
		t.Errorf("pg_safeguard owner = %q, want pg_remove", got)
	}
	if len(byKey) != 7 {
		t.Errorf("parsed %d params, want 7: %v", len(byKey), byKey)
	}
	if moduleOf(byKey["pg_data"].Role) != "PGSQL" || moduleOf("vibe") != "OTHER" {
		t.Errorf("module mapping is wrong")
	}
	if got := orderModules(map[string]bool{"PGSQL": true, "INFRA": true, "ZZZ": true}); len(got) != 3 || got[0] != "INFRA" || got[2] != "ZZZ" {
		t.Errorf("orderModules = %v", got)
	}
}

func TestBuildSchemaMergesOverrides(t *testing.T) {
	params := []param{
		{Key: "pg_cluster", Role: "pgsql", Section: "PG_IDENTITY", Level: "CLUSTER", Type: "string"},
		{Key: "pg_conf", Role: "pgsql", Type: "string", Default: "oltp.yml", HasDefault: true},
	}
	enum := []any{"oltp.yml", "olap.yml"}
	identity := true
	label := "PG 集群名"
	ov := &overrides{
		Params: map[string]paramOverride{
			"pg_cluster": {Identity: &identity, Label: &label},
			"pg_conf":    {Enum: &enum},
			"app":        {Module: strPtr("APP"), Type: strPtr("string")},
		},
		Defs: map[string]any{"pg_user": map[string]any{"type": "object"}},
	}
	doc := buildSchema(params, ov, "v4.5.0")
	if doc.Version != "v4.5.0" || len(doc.Defs) != 1 {
		t.Fatalf("unexpected document: %+v", doc)
	}
	if p := doc.Params["pg_cluster"]; !p.Identity || p.Label != "PG 集群名" || p.Module != "PGSQL" {
		t.Errorf("pg_cluster override not applied: %+v", p)
	}
	if p := doc.Params["pg_conf"]; len(p.Enum) != 2 || string(p.Default) != `"oltp.yml"` {
		t.Errorf("pg_conf = %+v", p)
	}
	if p := doc.Params["app"]; p.Module != "APP" || p.Type != "string" {
		t.Errorf("new override param = %+v", p)
	}
	joined := ""
	for _, m := range doc.Modules {
		joined += m + ","
	}
	if joined != "PGSQL,APP," {
		t.Errorf("modules = %q, want PGSQL,APP", joined)
	}
}

// strPtr returns a pointer to a string literal.
func strPtr(s string) *string { return &s }
