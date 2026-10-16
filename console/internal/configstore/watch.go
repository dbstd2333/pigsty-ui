package configstore

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// externalDebounce is the quiet period before an external change is committed.
const externalDebounce = 2 * time.Second

// externalMessage is the commit message of changes made outside the console.
const externalMessage = "external change (detected by console)"

// Watch commits changes made to pigsty.yml outside the console. It returns
// after the watcher is registered; callbacks run in a background goroutine.
func (s *Store) Watch(ctx context.Context) error {
	s.mu.Lock()
	alreadyWatching := s.watching
	s.watching = true
	s.mu.Unlock()
	if alreadyWatching {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(s.home); err != nil {
		watcher.Close()
		return err
	}
	go s.watchLoop(ctx, watcher)
	return nil
}

// SyncExternal commits a divergence produced while the console was stopped.
func (s *Store) SyncExternal(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, hash, err := readText(s.file)
	if err != nil {
		s.logger.Warn("cannot read configuration", "err", err)
		return
	}
	if headText, err := s.git.fileAt(ctx, "HEAD"); err == nil && hashText(headText) == hash {
		s.seenHash = hash
		return
	}
	s.commitExternalLocked(ctx, hash)
}

// watchLoop debounces file events and commits external modifications.
func (s *Store) watchLoop(ctx context.Context, watcher *fsnotify.Watcher) {
	defer watcher.Close()
	var (
		timer *time.Timer
		fire  <-chan time.Time
	)
	stop := func() {
		if timer != nil {
			timer.Stop()
		}
		fire = nil
	}
	for {
		select {
		case <-ctx.Done():
			stop()
			return
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			s.logger.Warn("config watcher error", "err", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) != filepath.Base(s.file) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(externalDebounce)
			} else {
				timer.Reset(externalDebounce)
			}
			fire = timer.C
		case <-fire:
			fire = nil
			s.commitExternal(ctx)
		}
	}
}

// commitExternal commits the current file content when it differs from what
// the console wrote last.
func (s *Store) commitExternal(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, hash, err := readText(s.file)
	if err != nil {
		s.logger.Warn("config watcher cannot read configuration", "err", err)
		return
	}
	if hash == s.seenHash {
		// The console wrote this content itself, nothing to record.
		return
	}
	s.commitExternalLocked(ctx, hash)
}

// commitExternalLocked records an external change; the caller holds the lock.
func (s *Store) commitExternalLocked(ctx context.Context, hash string) {
	commit, err := s.git.commit(ctx, externalMessage, AuthorExternal)
	if err != nil {
		s.logger.Error("cannot commit external change", "err", err)
		return
	}
	s.seenHash = hash
	s.version = ""
	s.logger.Info("external change committed", "hash", hash, "commit", commit.ShortHash)
}
