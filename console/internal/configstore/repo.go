package configstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Repo is the dedicated git repository that versions pigsty.yml. Its git dir
// lives outside the Pigsty home, so the upstream repository is never touched.
type Repo struct {
	git *gitRepo
}

// InitRepo opens the repository, creating it when missing, and returns a handle
// the CLI can use to create the first commit.
func InitRepo(ctx context.Context, workTree, gitDir string) (*Repo, error) {
	home, err := filepath.Abs(workTree)
	if err != nil {
		return nil, fmt.Errorf("resolve work tree: %w", err)
	}
	if _, err := os.Stat(filepath.Join(home, "pigsty.yml")); err != nil {
		return nil, fmt.Errorf("pigsty.yml not found in %s: %w", home, err)
	}
	dir, err := filepath.Abs(gitDir)
	if err != nil {
		return nil, fmt.Errorf("resolve git dir: %w", err)
	}
	g := &gitRepo{dir: dir, tree: home, rel: "pigsty.yml"}
	if err := g.init(ctx); err != nil {
		return nil, err
	}
	return &Repo{git: g}, nil
}

// HasCommits reports whether the repository already has a commit.
func (r *Repo) HasCommits() bool { return r.git.hasCommits(context.Background()) }

// Commit records the current content of pigsty.yml.
func (r *Repo) Commit(ctx context.Context, message, author string) (Commit, error) {
	return r.git.commit(ctx, message, author)
}
