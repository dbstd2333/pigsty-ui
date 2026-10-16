// Package validate runs the upstream bin/validate script and turns its output
// into structured issues.
package validate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Issue is one validation problem, shared with the HTTP API.
type Issue struct {
	Level   string `json:"level" enum:"error,warning" doc:"error blocks saving, warning does not"`
	Source  string `json:"source" enum:"yaml,schema,semantic,validate,inventory"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty" doc:"config path, e.g. all.children.pg-meta.vars.pg_users[0].name"`
	Line    *int   `json:"line,omitempty" doc:"1-based line number when known"`
	Column  *int   `json:"column,omitempty"`
}

// Detail returns the huma error detail of an issue.
func (i Issue) Detail() string {
	if i.Path == "" {
		return i.Message
	}
	return fmt.Sprintf("%s (%s)", i.Message, i.Path)
}

// Runner executes bin/validate with python3.
type Runner struct {
	home   string
	python string
	ok     bool
}

var (
	statusPattern = regexp.MustCompile(`^\[(OK|WARN|ERRO|ERROR|FAIL)\]\s*(.*)$`)
	quotedPattern = regexp.MustCompile("'([^'\\s]+)'|`([^`\\s]+)`")
	pathPattern   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-\[\]$]*$`)
)

// NewRunner probes the Pigsty home for an executable validate script.
func NewRunner(home string) *Runner {
	r := &Runner{home: home}
	script := filepath.Join(home, "bin", "validate")
	if st, err := os.Stat(script); err != nil || st.IsDir() {
		return r
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return r
	}
	r.python = python
	r.ok = true
	return r
}

// Available reports whether bin/validate can be executed.
func (r *Runner) Available() bool { return r.ok }

// Run executes bin/validate against a file. It returns no issues when the
// script is unavailable, so callers can degrade gracefully.
func (r *Runner) Run(ctx context.Context, file string) ([]Issue, error) {
	if !r.ok {
		return nil, nil
	}
	script := filepath.Join(r.home, "bin", "validate")
	cmd := exec.CommandContext(ctx, r.python, script, file)
	cmd.Dir = r.home
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	issues := Parse(stdout.String())
	if err == nil {
		return issues, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return issues, fmt.Errorf("run bin/validate: %w", err)
	}
	if !hasBlocking(issues) {
		// The script failed without a parseable [ERRO] line; surface its
		// message so the user still sees why validation failed.
		issues = append(issues, Issue{
			Level:   "error",
			Source:  "validate",
			Message: failureMessage(stderr.String(), fmt.Sprintf("bin/validate exited with code %d", exitErr.ExitCode())),
		})
	}
	return issues, nil
}

// Parse turns bin/validate stdout into issues.
func Parse(out string) []Issue {
	var issues []Issue
	failed := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := statusPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		status, msg := m[1], strings.TrimSpace(m[2])
		switch status {
		case "OK":
			continue
		case "WARN":
			issues = append(issues, newIssue("warning", msg))
		case "FAIL":
			failed = true
		default: // ERRO / ERROR
			issues = append(issues, newIssue("error", msg))
		}
	}
	if failed && !hasBlocking(issues) {
		issues = append(issues, Issue{Level: "error", Source: "validate", Message: "bin/validate failed"})
	}
	return issues
}

// newIssue builds an issue and extracts a config path from the message.
func newIssue(level, msg string) Issue {
	iss := Issue{Level: level, Source: "validate", Message: msg}
	if p := extractPath(msg); p != "" {
		iss.Path = p
	}
	return iss
}

// extractPath pulls the first config path quoted in a message, e.g.
// `all.children.pg-meta.vars.pg_cluster`.
func extractPath(msg string) string {
	for _, m := range quotedPattern.FindAllStringSubmatch(msg, -1) {
		for _, cand := range m[1:] {
			if cand == "" || !strings.Contains(cand, ".") {
				continue
			}
			if pathPattern.MatchString(cand) {
				return cand
			}
		}
	}
	return ""
}

func hasBlocking(issues []Issue) bool {
	for _, i := range issues {
		if i.Level == "error" {
			return true
		}
	}
	return false
}

// failureMessage picks the most useful line from the script's stderr.
func failureMessage(stderr, fallback string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "Traceback") || strings.HasPrefix(line, "  ") {
			continue
		}
		return strings.TrimPrefix(line, "Exception: ")
	}
	return fallback
}
