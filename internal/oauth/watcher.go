package oauth

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
)

// Watcher monitors the configured auth directory for Codex token file changes
// and reloads the AccountPool when relevant events occur.
//
// It debounces rapid file events (e.g. atomic saves) and tolerates invalid,
// unreadable, non-token, lock, and temp files without crashing.
// Missing auth directories are safe; the watcher will attempt to re-watch
// when the directory is created later.
type Watcher struct {
	mgr      *Manager
	pool     *AccountPool
	debounce time.Duration
	logger   *logrus.Logger

	fswatcher *fsnotify.Watcher
	done      chan struct{}
	closeOnce sync.Once

	// mu protects the debounce timer
	mu    sync.Mutex
	timer *time.Timer

	// watchingParent tracks whether we are currently watching the parent
	// directory (because the auth dir did not exist at startup). Once the
	// auth dir is created and added to the watcher, the parent watch is
	// removed to avoid spurious reload events from sibling entries.
	watchingParent string // empty string means not watching parent
}

// NewWatcher creates and starts a watcher for the auth directory configured
// on the given Manager. File events trigger a debounced reload of Codex
// tokens into the pool. The debounce interval controls how rapidly
// coalesced events settle before triggering a reload.
//
// If the auth directory does not exist, NewWatcher succeeds but watches
// the parent path; subsequent directory creation will be detected.
// Call Close() to stop the watcher and release resources.
func NewWatcher(mgr *Manager, pool *AccountPool, debounce time.Duration, logger ...*logrus.Logger) (*Watcher, error) {
	if mgr == nil || pool == nil {
		return nil, nil
	}
	if debounce <= 0 {
		debounce = 200 * time.Millisecond
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		mgr:       mgr,
		pool:      pool,
		debounce:  debounce,
		fswatcher: fsw,
		done:      make(chan struct{}),
	}
	if len(logger) > 0 && logger[0] != nil {
		w.logger = logger[0]
	} else {
		w.logger = logrus.New()
		w.logger.SetOutput(os.Stderr)
	}

	// Initial seed: load tokens and reload pool only if the pool is empty.
	// When the server has already seeded the pool at startup, this avoids
	// redundant file reads while keeping the behavior idempotent.
	w.seedPool()

	// Attempt to watch the auth dir (or parent if missing)
	w.watchAuthDir()

	go w.loop()

	return w, nil
}

// Close stops the watcher and releases resources. It is safe to call multiple times.
func (w *Watcher) Close() {
	w.closeOnce.Do(func() {
		close(w.done)
		w.mu.Lock()
		if w.timer != nil {
			w.timer.Stop()
		}
		w.mu.Unlock()
		_ = w.fswatcher.Close()
	})
}

// seedPool loads tokens from the auth dir and reloads the pool.
// If the pool already has Codex entries (e.g., seeded by server startup),
// the seed is skipped to avoid redundant file reads. The watcher's
// debounced reload will keep the pool current after this point.
// Invalid files are logged and skipped.
func (w *Watcher) seedPool() {
	if w.pool.EnabledCodexCount() > 0 {
		return
	}
	tokens, err := w.loadCodexTokensSafe()
	if err != nil {
		w.logger.WithError(err).Warn("watcher: initial token load failed")
		return
	}
	w.pool.Reload(tokens)
}

// loadCodexTokensSafe loads Codex tokens from the auth dir,
// tolerating missing directories and invalid files.
func (w *Watcher) loadCodexTokensSafe() ([]*Token, error) {
	dir, err := w.mgr.AuthDir()
	if err != nil {
		return nil, err
	}
	return LoadCodexTokensFromDir(w.mgr, dir, w.logger)
}

// watchAuthDir adds the auth dir (or its parent) to the fsnotify watcher.
func (w *Watcher) watchAuthDir() {
	dir, err := w.mgr.AuthDir()
	if err != nil {
		w.logger.WithError(err).Warn("watcher: cannot resolve auth dir")
		return
	}

	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		if err := w.fswatcher.Add(dir); err != nil {
			w.logger.WithError(err).WithField("dir", dir).Warn("watcher: cannot watch auth dir")
		}
	} else {
		// Directory doesn't exist; watch the parent so we detect creation
		parent := filepath.Dir(dir)
		if fi, err := os.Stat(parent); err == nil && fi.IsDir() {
			if err := w.fswatcher.Add(parent); err != nil {
				w.logger.WithError(err).WithField("dir", parent).Warn("watcher: cannot watch parent dir")
			} else {
				w.mu.Lock()
				w.watchingParent = parent
				w.mu.Unlock()
			}
		}
	}
}

// loop processes fsnotify events.
func (w *Watcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fswatcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.fswatcher.Errors:
			if !ok {
				return
			}
			if err != nil {
				w.logger.WithError(err).Warn("watcher: fsnotify error")
			}
		}
	}
}

// handleEvent processes a single fsnotify event.
func (w *Watcher) handleEvent(event fsnotify.Event) {
	name := filepath.Base(event.Name)

	// Check if this event is relevant
	switch {
	case event.Has(fsnotify.Create):
		// A new file or directory was created
		if w.isAuthDir(event.Name) {
			// The auth dir itself was created; start watching it
			_ = w.fswatcher.Add(event.Name)
			// Remove the parent-directory watch so sibling entries no
			// longer generate spurious reload events.
			w.removeParentWatch()
			w.scheduleReload()
			return
		}
		if IsTokenFileName(name) {
			w.scheduleReload()
		}
		// Non-token files are silently ignored

	case event.Has(fsnotify.Write):
		if IsTokenFileName(name) {
			w.scheduleReload()
		}

	case event.Has(fsnotify.Rename):
		if IsTokenFileName(name) {
			w.scheduleReload()
		}
		// Also handle the case where a file is renamed TO a token name
		// (fsnotify may send a separate Create event for the destination)

	case event.Has(fsnotify.Remove):
		if w.isAuthDir(event.Name) {
			// Auth dir was removed; re-watch parent
			w.scheduleReload()
			return
		}
		if IsTokenFileName(name) {
			w.scheduleReload()
		}

	case event.Has(fsnotify.Chmod):
		// Permission changes are not relevant for reload
	}
}

// removeParentWatch removes the parent-directory watch if one is active,
// preventing spurious reload events from sibling entries.
func (w *Watcher) removeParentWatch() {
	w.mu.Lock()
	parent := w.watchingParent
	w.watchingParent = ""
	w.mu.Unlock()

	if parent != "" {
		_ = w.fswatcher.Remove(parent)
	}
}

// scheduleReload debounces reload events.
func (w *Watcher) scheduleReload() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, func() {
		w.reload()
	})
}

// reload loads tokens from the auth dir and reloads the pool.
// Invalid files are logged and skipped. Missing directories result in an
// empty token set, effectively clearing removed entries.
func (w *Watcher) reload() {
	tokens, err := w.loadCodexTokensSafe()
	if err != nil {
		w.logger.WithError(err).Warn("watcher: reload failed")
		return
	}
	w.pool.Reload(tokens)

	// Re-ensure we're watching the auth dir (it may have been recreated)
	dir, err := w.mgr.AuthDir()
	if err != nil {
		return
	}
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		_ = w.fswatcher.Add(dir)
	}
}

// isAuthDir checks if the given path is the configured auth dir using
// symlink-safe comparison via os.SameFile.
func (w *Watcher) isAuthDir(path string) bool {
	dir, err := w.mgr.AuthDir()
	if err != nil {
		return false
	}
	// Use os.SameFile for symlink-safe comparison
	pathInfo, err := os.Stat(path)
	if err != nil {
		return false
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return os.SameFile(pathInfo, dirInfo)
}
