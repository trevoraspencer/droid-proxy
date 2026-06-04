package oauth

import (
	"os"
	"path/filepath"
	"strings"
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

	// Initial seed: load tokens and reload pool
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
// Invalid files are logged and skipped.
func (w *Watcher) seedPool() {
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

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var tokens []*Token
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isTokenFileName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		tok, err := w.mgr.loadTokenPath(path)
		if err != nil {
			w.logger.WithError(err).WithField("file", name).Warn("watcher: skipping invalid token file")
			continue
		}
		if tok.Provider() == ProviderCodex {
			tokens = append(tokens, tok)
		}
	}
	return tokens, nil
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
			w.fswatcher.Add(event.Name)
			w.scheduleReload()
			return
		}
		if isTokenFileName(name) {
			w.scheduleReload()
		}
		// Non-token files are silently ignored

	case event.Has(fsnotify.Write):
		if isTokenFileName(name) {
			w.scheduleReload()
		}

	case event.Has(fsnotify.Rename):
		if isTokenFileName(name) {
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
		if isTokenFileName(name) {
			w.scheduleReload()
		}

	case event.Has(fsnotify.Chmod):
		// Permission changes are not relevant for reload
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

// isAuthDir checks if the given path is the configured auth dir.
func (w *Watcher) isAuthDir(path string) bool {
	dir, err := w.mgr.AuthDir()
	if err != nil {
		return false
	}
	return path == dir
}

// isTokenFileName returns true if the filename looks like a token file
// that should be processed. It excludes:
//   - Non-.json files
//   - Hidden files (starting with .)
//   - Lock files (.locks/ directory children or .lock extension)
//   - Atomic-save temp files (.<name>.tmp-* pattern)
func isTokenFileName(name string) bool {
	// Must end with .json
	if filepath.Ext(name) != ".json" {
		return false
	}
	// Skip hidden files (e.g. .codex-user.json.tmp-12345)
	if strings.HasPrefix(name, ".") {
		return false
	}
	// Skip lock files
	if strings.HasSuffix(name, ".lock") {
		return false
	}
	return true
}
