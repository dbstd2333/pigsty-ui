package validate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseIssues(t *testing.T) {
	out := `[ OK ] all.vars
[WARN] 'all.vars.version' not found, is this a pigsty config file?
[ERRO] ` + "`all.children.pg-meta.vars.pg_users[0]`" + ` should have a 'name'
[ERROR] 'all.children.infra.hosts.10.10.10.10.infra_seq' not found
[FAIL] all.vars
`
	issues := Parse(out)
	if len(issues) != 3 {
		t.Fatalf("parsed %d issues, want 3: %+v", len(issues), issues)
	}
	if issues[0].Level != "warning" || issues[0].Source != "validate" {
		t.Errorf("first issue = %+v", issues[0])
	}
	if issues[1].Level != "error" || issues[1].Path != "all.children.pg-meta.vars.pg_users[0]" {
		t.Errorf("second issue = %+v", issues[1])
	}
	if issues[2].Level != "error" || issues[2].Path != "all.children.infra.hosts.10.10.10.10.infra_seq" {
		t.Errorf("third issue = %+v", issues[2])
	}
}

func TestParseFailureWithoutDetail(t *testing.T) {
	issues := Parse("[FAIL] all.vars\n")
	if len(issues) != 1 || issues[0].Level != "error" {
		t.Fatalf("issues = %+v, want one blocking error", issues)
	}
}

func TestExtractPath(t *testing.T) {
	cases := map[string]string{
		"'all.vars.admin_ip' not found":                        "all.vars.admin_ip",
		"`pgsql.pg-meta.vars.pg_hba_rules[0].auth` is invalid": "pgsql.pg-meta.vars.pg_hba_rules[0].auth",
		"some message without a path":                          "",
	}
	for msg, want := range cases {
		if got := extractPath(msg); got != want {
			t.Errorf("extractPath(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestRunnerUnavailable(t *testing.T) {
	runner := NewRunner(t.TempDir())
	if runner.Available() {
		t.Fatal("runner without bin/validate should be unavailable")
	}
	issues, err := runner.Run(context.Background(), "whatever.yml")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
}

func TestRunnerExecutesScript(t *testing.T) {
	home := t.TempDir()
	script := "#!/usr/bin/env python3\nimport sys\nprint(\"[ERRO] 'all.vars.admin_ip' is not a valid ipv4 address\")\nsys.exit(1)\n"
	if err := writeFile(filepath.Join(home, "bin", "validate"), script); err != nil {
		t.Fatalf("write script: %v", err)
	}
	runner := NewRunner(home)
	if !runner.Available() {
		t.Skip("python3 is unavailable")
	}
	issues, err := runner.Run(context.Background(), filepath.Join(home, "pigsty.yml"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(issues) != 1 || issues[0].Path != "all.vars.admin_ip" {
		t.Fatalf("issues = %+v", issues)
	}
}

// writeFile creates a file with its parent directories.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o755)
}
