// Package configstore owns pigsty.yml: file writes with optimistic locking,
// validation, git history and fsnotify based external change detection.
package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/validate"
)

// DefaultMessage is used when a client saves without a commit message.
const DefaultMessage = "update pigsty.yml"

// ConflictError reports that baseHash no longer matches the file on disk.
type ConflictError struct {
	CurrentText string `json:"currentText"`
	CurrentHash string `json:"currentHash"`
}

// Error implements the error interface.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("configuration changed on disk (current hash %s)", e.CurrentHash)
}

// ValidationError reports blocking validation issues; nothing was written.
type ValidationError struct {
	Issues []validate.Issue `json:"issues"`
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("%d validation issue(s)", len(e.Issues))
}

// SaveRequest is one write through the API.
type SaveRequest struct {
	Text     string
	BaseHash string
	Message  string
	Author   string
}

// SaveResult is the response of a successful write.
type SaveResult struct {
	Hash   string `json:"hash"`
	Commit Commit `json:"commit"`
}

// Options configures a Store.
type Options struct {
	Home      string
	DataDir   string
	Logger    *slog.Logger
	Inventory *inventory.Provider
}

// Store serializes access to pigsty.yml and its git history.
type Store struct {
	home   string
	file   string
	logger *slog.Logger
	git    *gitRepo

	validator *validate.Runner
	inv       *inventory.Provider

	mu       sync.Mutex
	seenHash string
	version  string
	watching bool
}

// Open creates a store for an initialized Pigsty home and data directory.
func Open(opts Options) (*Store, error) {
	home, err := filepath.Abs(opts.Home)
	if err != nil {
		return nil, fmt.Errorf("resolve pigsty home: %w", err)
	}
	file := filepath.Join(home, "pigsty.yml")
	if _, err := os.Stat(file); err != nil {
		return nil, fmt.Errorf("pigsty.yml not found in %s: %w", home, err)
	}
	gitDir := filepath.Join(opts.DataDir, "config.git")
	g := &gitRepo{dir: gitDir, tree: home, rel: "pigsty.yml"}
	if !g.exists() {
		return nil, fmt.Errorf("git repository %s not found, run `pigsty-console init` first", gitDir)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	inv := opts.Inventory
	if inv == nil {
		inv = inventory.NewProvider(home)
	}
	_, hash, err := readText(file)
	if err != nil {
		return nil, err
	}
	return &Store{
		home:      home,
		file:      file,
		logger:    logger,
		git:       g,
		validator: validate.NewRunner(home),
		inv:       inv,
		seenHash:  hash,
	}, nil
}

// Home returns the Pigsty home directory.
func (s *Store) Home() string { return s.home }

// File returns the path of the tracked configuration file.
func (s *Store) File() string { return s.file }

// Validator exposes the bin/validate runner for /meta.
func (s *Store) Validator() *validate.Runner { return s.validator }

// Inventory exposes the inventory provider for /meta.
func (s *Store) Inventory() *inventory.Provider { return s.inv }

// Current returns the file content as it is on disk, together with the HEAD
// commit. Uncommitted external changes are reported with their own hash.
func (s *Store) Current(ctx context.Context) (string, string, *Commit, error) {
	text, hash, err := readText(s.file)
	if err != nil {
		return "", "", nil, err
	}
	commit, err := s.git.lastCommit(ctx)
	if err != nil {
		if errors.Is(err, ErrCommitNotFound) {
			return text, hash, nil, nil
		}
		return "", "", nil, err
	}
	return text, hash, &commit, nil
}

// Save validates and writes new content, then records it as a git commit.
func (s *Store) Save(ctx context.Context, req SaveRequest) (SaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	curText, curHash, err := readText(s.file)
	if err != nil {
		return SaveResult{}, err
	}
	if req.BaseHash != "" && req.BaseHash != curHash {
		return SaveResult{}, &ConflictError{CurrentText: curText, CurrentHash: curHash}
	}
	if req.Text == curText {
		// Nothing changed: report the current version, do not create a commit.
		if commit, err := s.git.lastCommit(ctx); err == nil {
			return SaveResult{Hash: curHash, Commit: commit}, nil
		}
	}

	issues, err := s.validateText(ctx, req.Text)
	if err != nil {
		return SaveResult{}, err
	}
	if HasErrors(issues) {
		return SaveResult{}, &ValidationError{Issues: issues}
	}
	if err := s.writeFile(req.Text); err != nil {
		return SaveResult{}, err
	}

	author := req.Author
	if author == "" {
		author = AuthorExternal
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = DefaultMessage
	}
	commit, err := s.git.commit(ctx, message, author)
	if err != nil {
		return SaveResult{}, fmt.Errorf("configuration written but commit failed: %w", err)
	}
	hash := hashText(req.Text)
	s.seenHash = hash
	s.version = ""
	s.logger.Info("configuration saved", "hash", hash, "commit", commit.ShortHash, "author", author)
	return SaveResult{Hash: hash, Commit: commit}, nil
}

// Revert writes the content of an earlier commit as a new commit.
func (s *Store) Revert(ctx context.Context, hash, baseHash, message, author string) (SaveResult, error) {
	text, err := s.git.fileAt(ctx, hash)
	if err != nil {
		return SaveResult{}, err
	}
	if strings.TrimSpace(message) == "" {
		short := hash
		if len(short) > 8 {
			short = short[:8]
		}
		message = "revert to " + short
	}
	return s.Save(ctx, SaveRequest{Text: text, BaseHash: baseHash, Message: message, Author: author})
}

// History returns commits newest first plus the total commit count.
func (s *Store) History(ctx context.Context, limit, offset int) ([]Commit, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	if !s.git.hasCommits(ctx) {
		return []Commit{}, 0, nil
	}
	return s.git.history(ctx, limit, offset)
}

// Show returns one commit with the file content at that commit.
func (s *Store) Show(ctx context.Context, hash string) (Commit, string, error) {
	return s.git.show(ctx, hash)
}

// DiffResult is the response of GET /config/diff.
type DiffResult struct {
	From     string `json:"from"`
	To       string `json:"to"`
	FromText string `json:"fromText"`
	ToText   string `json:"toText"`
	Unified  string `json:"unified"`
}

// Diff compares two commits of the configuration file.
func (s *Store) Diff(ctx context.Context, from, to string) (DiffResult, error) {
	fromText, err := s.git.fileAt(ctx, from)
	if err != nil {
		return DiffResult{}, err
	}
	toText, err := s.git.fileAt(ctx, to)
	if err != nil {
		return DiffResult{}, err
	}
	unified, err := s.git.diff(ctx, from, to)
	if err != nil {
		return DiffResult{}, err
	}
	return DiffResult{From: from, To: to, FromText: fromText, ToText: toText, Unified: unified}, nil
}

// ValidateText runs the authoritative checks on candidate content without
// touching the configuration file.
func (s *Store) ValidateText(ctx context.Context, text string) ([]validate.Issue, error) {
	return s.validateText(ctx, text)
}

// PigstyVersion returns all.vars.version of the current configuration.
func (s *Store) PigstyVersion() string {
	s.mu.Lock()
	text, hash, err := readText(s.file)
	if err != nil {
		s.mu.Unlock()
		return ""
	}
	if hash == s.seenHash && s.version != "" {
		version := s.version
		s.mu.Unlock()
		return version
	}
	s.mu.Unlock()

	var doc struct {
		All struct {
			Vars map[string]any `yaml:"vars"`
		} `yaml:"all"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return ""
	}
	version, _ := doc.All.Vars["version"].(string)

	s.mu.Lock()
	if hash == s.seenHash {
		s.version = version
	}
	s.mu.Unlock()
	return version
}

// validateText writes the candidate to a temporary file next to pigsty.yml and
// runs bin/validate plus the inventory parsers on it.
func (s *Store) validateText(ctx context.Context, text string) ([]validate.Issue, error) {
	tmp, err := s.writeTemp(text)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)

	var issues []validate.Issue
	scriptIssues, err := s.validator.Run(ctx, tmp)
	if err != nil {
		return nil, err
	}
	issues = append(issues, scriptIssues...)

	data, err := os.ReadFile(tmp)
	if err != nil {
		return nil, fmt.Errorf("read temp file: %w", err)
	}
	if _, err := inventory.Load(data); err != nil {
		var le *inventory.LoadError
		if errors.As(err, &le) {
			issues = append(issues, issueFromLoadError(le))
		} else {
			issues = append(issues, validate.Issue{Level: "error", Source: "yaml", Message: err.Error()})
		}
	} else if err := s.inv.CheckFile(ctx, tmp); err != nil {
		// Ansible accepted the file as a valid inventory when it is available.
		issues = append(issues, validate.Issue{Level: "error", Source: "inventory", Message: err.Error()})
	}
	return issues, nil
}

// writeFile atomically replaces pigsty.yml, keeping its file mode.
func (s *Store) writeFile(text string) error {
	tmp, err := s.writeTemp(text)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if st, err := os.Stat(s.file); err == nil {
		if err := os.Chmod(tmp, st.Mode().Perm()); err != nil {
			return fmt.Errorf("chmod temp file: %w", err)
		}
	}
	if err := os.Rename(tmp, s.file); err != nil {
		return fmt.Errorf("replace %s: %w", s.file, err)
	}
	return nil
}

// writeTemp stores content in a hidden temporary file inside the Pigsty home,
// so that the later rename stays on the same filesystem.
func (s *Store) writeTemp(text string) (string, error) {
	f, err := os.CreateTemp(s.home, ".pigsty-console-*.yml")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	name := f.Name()
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("close temp file: %w", err)
	}
	return name, nil
}

// issueFromLoadError converts an inventory parse failure into an issue.
func issueFromLoadError(le *inventory.LoadError) validate.Issue {
	iss := validate.Issue{Level: "error", Source: le.Source, Message: le.Message}
	if le.Line > 0 {
		line := le.Line
		iss.Line = &line
	}
	if le.Column > 0 {
		column := le.Column
		iss.Column = &column
	}
	return iss
}

// HasErrors reports whether any issue blocks a save.
func HasErrors(issues []validate.Issue) bool {
	for _, i := range issues {
		if i.Level == "error" {
			return true
		}
	}
	return false
}

// readText reads a file and returns its content plus sha256 hash.
func readText(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), hashText(string(data)), nil
}

// hashText returns the sha256 hex digest of a configuration text.
func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// FirstCommit returns the oldest commit of the configuration history. It is the
// applied baseline of clusters that were never touched by the console.
func (s *Store) FirstCommit(ctx context.Context) (string, *time.Time, error) {
	commit, err := s.git.firstCommit(ctx)
	if err != nil {
		return "", nil, err
	}
	at := commit.Time
	return commit.Hash, &at, nil
}
