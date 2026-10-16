package configstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Commit is one version of the configuration file.
type Commit struct {
	Hash      string    `json:"hash" doc:"git commit sha"`
	ShortHash string    `json:"shortHash"`
	Author    string    `json:"author" doc:"console username or external"`
	Message   string    `json:"message"`
	Time      time.Time `json:"time" doc:"RFC 3339 commit time"`
}

// AuthorExternal marks commits that were not made through the console.
const AuthorExternal = "external"

// ErrCommitNotFound is returned when a commit hash does not exist.
var ErrCommitNotFound = errors.New("commit not found")

// refPattern accepts full or abbreviated commit hashes plus HEAD.
var refPattern = regexp.MustCompile(`^([0-9a-fA-F]{4,40}|HEAD)$`)

const recordSep = "\x1f"

// gitRepo runs the system git CLI against an external git dir and work tree so
// that the Pigsty repository's own .git directory is never touched.
type gitRepo struct {
	dir  string
	tree string
	rel  string
}

// run executes git with the repo options and returns stdout.
func (g *gitRepo) run(ctx context.Context, args ...string) (string, error) {
	return g.runEnv(ctx, nil, args...)
}

// runEnv executes git with extra environment variables.
func (g *gitRepo) runEnv(ctx context.Context, env []string, args ...string) (string, error) {
	full := []string{
		"-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null",
		"--git-dir=" + g.dir,
		"--work-tree=" + g.tree,
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = g.tree
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// init creates the repository. It is a no-op when the repo already exists.
func (g *gitRepo) init(ctx context.Context) error {
	if _, err := os.Stat(g.dir); err == nil {
		return nil
	}
	if _, err := g.run(ctx, "init"); err != nil {
		return err
	}
	return nil
}

// exists reports whether the repository directory is usable.
func (g *gitRepo) exists() bool {
	st, err := os.Stat(g.dir)
	return err == nil && st.IsDir()
}

// hasCommits reports whether HEAD already points at a commit.
func (g *gitRepo) hasCommits(ctx context.Context) bool {
	_, err := g.run(ctx, "rev-parse", "--verify", "HEAD")
	return err == nil
}

// commit stages the tracked file and creates a commit for the given author.
func (g *gitRepo) commit(ctx context.Context, message, author string) (Commit, error) {
	if _, err := g.run(ctx, "add", "--", g.rel); err != nil {
		return Commit{}, err
	}
	email := fmt.Sprintf("%s@pigsty-console", sanitizeEmailLocal(author))
	env := []string{
		"GIT_AUTHOR_NAME=" + author,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + author,
		"GIT_COMMITTER_EMAIL=" + email,
	}
	if _, err := g.runEnv(ctx, env, "commit", "-m", message, "--", g.rel); err != nil {
		return Commit{}, err
	}
	head, err := g.lastCommit(ctx)
	if err != nil {
		return Commit{}, err
	}
	return head, nil
}

// lastCommit returns HEAD, or an error when the history is empty.
func (g *gitRepo) lastCommit(ctx context.Context) (Commit, error) {
	out, err := g.run(ctx, "log", "-n", "1", "--format="+logFormat)
	if err != nil {
		return Commit{}, err
	}
	commits, err := parseLog(out)
	if err != nil {
		return Commit{}, err
	}
	if len(commits) == 0 {
		return Commit{}, ErrCommitNotFound
	}
	return commits[0], nil
}

const logFormat = "%H" + recordSep + "%h" + recordSep + "%an" + recordSep + "%aI" + recordSep + "%s"

// history returns commits newest first plus the total commit count.
func (g *gitRepo) history(ctx context.Context, limit, offset int) ([]Commit, int, error) {
	out, err := g.run(ctx, "log", "--format="+logFormat,
		"-n", strconv.Itoa(limit), "--skip="+strconv.Itoa(offset))
	if err != nil {
		return nil, 0, err
	}
	commits, err := parseLog(out)
	if err != nil {
		return nil, 0, err
	}
	totalOut, err := g.run(ctx, "rev-list", "--count", "HEAD")
	if err != nil {
		return nil, 0, err
	}
	total, err := strconv.Atoi(strings.TrimSpace(totalOut))
	if err != nil {
		return nil, 0, fmt.Errorf("parse commit count: %w", err)
	}
	return commits, total, nil
}

// show returns one commit plus the tracked file content at that commit.
func (g *gitRepo) show(ctx context.Context, ref string) (Commit, string, error) {
	if !ValidRef(ref) {
		return Commit{}, "", ErrCommitNotFound
	}
	out, err := g.run(ctx, "log", "-n", "1", "--format="+logFormat, ref)
	if err != nil {
		return Commit{}, "", ErrCommitNotFound
	}
	commits, err := parseLog(out)
	if err != nil || len(commits) == 0 {
		return Commit{}, "", ErrCommitNotFound
	}
	text, err := g.fileAt(ctx, ref)
	if err != nil {
		return Commit{}, "", err
	}
	return commits[0], text, nil
}

// fileAt returns the tracked file content at a ref.
func (g *gitRepo) fileAt(ctx context.Context, ref string) (string, error) {
	if !ValidRef(ref) {
		return "", ErrCommitNotFound
	}
	out, err := g.run(ctx, "show", ref+":"+g.rel)
	if err != nil {
		return "", ErrCommitNotFound
	}
	return out, nil
}

// diff returns the unified diff between two refs.
func (g *gitRepo) diff(ctx context.Context, from, to string) (string, error) {
	if !ValidRef(from) || !ValidRef(to) {
		return "", ErrCommitNotFound
	}
	return g.run(ctx, "diff", "--no-color", "-U3", from, to, "--", g.rel)
}

// parseLog parses git log output written with recordSep separated fields.
func parseLog(out string) ([]Commit, error) {
	var commits []Commit
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, recordSep)
		if len(fields) < 5 {
			return nil, fmt.Errorf("parse git log line: %q", line)
		}
		t, err := time.Parse(time.RFC3339, fields[3])
		if err != nil {
			return nil, fmt.Errorf("parse commit time %q: %w", fields[3], err)
		}
		commits = append(commits, Commit{
			Hash:      fields[0],
			ShortHash: fields[1],
			Author:    fields[2],
			Time:      t,
			Message:   strings.Join(fields[4:], recordSep),
		})
	}
	return commits, nil
}

// ValidRef reports whether a client supplied ref is safe to pass to git.
func ValidRef(ref string) bool { return refPattern.MatchString(ref) }

// sanitizeEmailLocal keeps an author name usable as an email local part.
func sanitizeEmailLocal(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "console"
	}
	return out
}

// firstCommit returns the oldest commit of the configuration history.
func (g *gitRepo) firstCommit(ctx context.Context) (Commit, error) {
	out, err := g.run(ctx, "log", "--reverse", "-n", "1", "--format="+logFormat)
	if err != nil {
		return Commit{}, err
	}
	commits, err := parseLog(out)
	if err != nil {
		return Commit{}, err
	}
	if len(commits) == 0 {
		return Commit{}, ErrCommitNotFound
	}
	return commits[0], nil
}
