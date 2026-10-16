package schema

import (
	"testing"

	schemafiles "github.com/dbstd2333/pigsty-ui/console/schema"
)

// loadGenerated loads the committed schema document.
func loadGenerated(t *testing.T, version string) *Set {
	t.Helper()
	set, err := Load(schemafiles.Files, version)
	if err != nil {
		t.Fatalf("load schema %q: %v", version, err)
	}
	return set
}

func TestLoadGeneratedSchema(t *testing.T) {
	set := loadGenerated(t, "v4.5.0")
	if set.File.Version != "v4.5.0" {
		t.Errorf("version = %q, want v4.5.0", set.File.Version)
	}
	if len(set.File.Params) < 300 {
		t.Errorf("only %d params generated", len(set.File.Params))
	}
	cluster, ok := set.File.Params["pg_cluster"]
	if !ok {
		t.Fatal("pg_cluster is missing from the schema")
	}
	if !cluster.Identity || cluster.Level != "CLUSTER" || cluster.Type != "string" || cluster.Module != "PGSQL" {
		t.Errorf("pg_cluster = %+v", cluster)
	}
	if len(cluster.Default) != 0 {
		t.Errorf("pg_cluster must not have a default, got %s", cluster.Default)
	}
	users, ok := set.File.Params["pg_users"]
	if !ok || users.Items == nil {
		t.Fatalf("pg_users = %+v, want items with a $ref", users)
	}
	if _, ok := set.File.Defs["pg_user"]; !ok {
		t.Error("defs.pg_user is missing")
	}
	for _, module := range []string{"INFRA", "NODE", "PGSQL", "ETCD", "MINIO", "REDIS", "DOCKER", "APP"} {
		var found bool
		for _, m := range set.File.Modules {
			if m == module {
				found = true
			}
		}
		if !found {
			t.Errorf("module %s missing from %v", module, set.File.Modules)
		}
	}
}

func TestVersionFallback(t *testing.T) {
	// A bare version and an unknown version must both resolve to the newest
	// embedded schema instead of failing.
	for _, version := range []string{"4.5.0", "v9.9.9", ""} {
		set := loadGenerated(t, version)
		if set.File.Version != "v4.5.0" {
			t.Errorf("version %q resolved to %q, want v4.5.0", version, set.File.Version)
		}
	}
}

func TestSensitiveAndDefaults(t *testing.T) {
	set := loadGenerated(t, "v4.5.0")
	for key, want := range map[string]bool{
		"pg_admin_password": true,
		"patroni_password":  true,
		"minio_secret_key":  true,
		"pg_port":           false,
		"admin_ip":          false,
	} {
		if got := set.Sensitive(key); got != want {
			t.Errorf("Sensitive(%q) = %v, want %v", key, got, want)
		}
	}
	if got := set.Sensitive("pg_users[0].password"); !got {
		t.Errorf("nested password key should be sensitive")
	}
	if value, ok := set.Default("pg_version"); !ok || value != float64(18) {
		t.Errorf("Default(pg_version) = %v,%v, want 18", value, ok)
	}
	if _, ok := set.Default("pg_cluster"); ok {
		t.Errorf("pg_cluster must not have a default")
	}
	defaults := set.Defaults()
	if len(defaults) < 200 {
		t.Errorf("only %d defaults collected", len(defaults))
	}
	if defaults["pg_mode"] != "pgsql" {
		t.Errorf("defaults[pg_mode] = %v, want pgsql", defaults["pg_mode"])
	}
}
