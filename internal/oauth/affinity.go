package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// affinityRecord is one conversation→account binding persisted on disk.
type affinityRecord struct {
	AccountPath string    `json:"account_path"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type affinityFile struct {
	Version int                       `json:"version"`
	Entries map[string]affinityRecord `json:"entries"`
}

const affinityFileVersion = 1

// AffinityStore maps Codex conversation IDs to token file paths.
type AffinityStore struct {
	mu      sync.RWMutex
	path    string
	entries map[string]affinityRecord
	ttl     time.Duration
	max     int
	nowFunc func() time.Time
}

// AffinityOptions configures conversation affinity persistence.
type AffinityOptions struct {
	Path       string
	TTL        time.Duration
	MaxEntries int
	NowFunc    func() time.Time
}

// NewAffinityStore loads or creates the affinity file at path.
func NewAffinityStore(opts AffinityOptions) (*AffinityStore, error) {
	if opts.NowFunc == nil {
		opts.NowFunc = time.Now
	}
	if opts.TTL <= 0 {
		opts.TTL = 720 * time.Hour
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = 10000
	}
	path, err := expandUserPath(strings.TrimSpace(opts.Path))
	if err != nil {
		return nil, fmt.Errorf("resolve affinity path: %w", err)
	}
	s := &AffinityStore{
		path:    path,
		entries: make(map[string]affinityRecord),
		ttl:     opts.TTL,
		max:     opts.MaxEntries,
		nowFunc: opts.NowFunc,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// DefaultAffinityPath returns the default affinity file path for an auth directory.
func DefaultAffinityPath(authDir string) string {
	parent := filepath.Dir(strings.TrimSpace(authDir))
	if parent == "" || parent == "." {
		return "~/.droid-proxy/conversation_affinity.json"
	}
	return filepath.Join(parent, "conversation_affinity.json")
}

// ResolveAffinityPath picks the configured path or default next to auth_dir.
func ResolveAffinityPath(cfg *config.Config, authDir string) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.OAuth.LoadBalancing.AffinityPath) != "" {
		return expandUserPath(cfg.OAuth.LoadBalancing.AffinityPath)
	}
	return expandUserPath(DefaultAffinityPath(authDir))
}

func (s *AffinityStore) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read affinity file: %w", err)
	}
	var file affinityFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parse affinity file: %w", err)
	}
	if file.Entries == nil {
		return nil
	}
	now := s.nowFunc()
	for id, rec := range file.Entries {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(rec.AccountPath) == "" {
			continue
		}
		if s.ttl > 0 && !rec.UpdatedAt.IsZero() && now.Sub(rec.UpdatedAt) > s.ttl {
			continue
		}
		s.entries[id] = rec
	}
	return nil
}

func (s *AffinityStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create affinity dir: %w", err)
	}
	file := affinityFile{Version: affinityFileVersion, Entries: s.entries}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize affinity: %w", err)
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(s.path, raw, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}

// Lookup returns the bound account path for a conversation, or "" if none.
func (s *AffinityStore) Lookup(conversationID string) string {
	if s == nil {
		return ""
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.entries[conversationID]
	if !ok {
		return ""
	}
	return rec.AccountPath
}

// Unbind removes a conversation binding and persists the updated map.
func (s *AffinityStore) Unbind(conversationID string) error {
	if s == nil {
		return nil
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[conversationID]; !ok {
		return nil
	}
	delete(s.entries, conversationID)
	return s.saveLocked()
}

// Bind associates a conversation with an account path and persists to disk.
func (s *AffinityStore) Bind(conversationID, accountPath string) error {
	if s == nil {
		return nil
	}
	conversationID = strings.TrimSpace(conversationID)
	accountPath = strings.TrimSpace(accountPath)
	if conversationID == "" || accountPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[conversationID] = affinityRecord{
		AccountPath: accountPath,
		UpdatedAt:   s.nowFunc().UTC(),
	}
	s.enforceMaxLocked()
	return s.saveLocked()
}

// Prune removes stale bindings: expired TTL, unknown account paths, over max entries.
func (s *AffinityStore) Prune(validPaths map[string]bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()
	for id, rec := range s.entries {
		if validPaths != nil && !validPaths[rec.AccountPath] {
			delete(s.entries, id)
			continue
		}
		if s.ttl > 0 && !rec.UpdatedAt.IsZero() && now.Sub(rec.UpdatedAt) > s.ttl {
			delete(s.entries, id)
		}
	}
	s.enforceMaxLocked()
	return s.saveLocked()
}

func (s *AffinityStore) enforceMaxLocked() {
	if s.max <= 0 || len(s.entries) <= s.max {
		return
	}
	type pair struct {
		id  string
		rec affinityRecord
	}
	all := make([]pair, 0, len(s.entries))
	for id, rec := range s.entries {
		all = append(all, pair{id, rec})
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].rec.UpdatedAt.Before(all[j].rec.UpdatedAt)
	})
	remove := len(all) - s.max
	for i := 0; i < remove; i++ {
		delete(s.entries, all[i].id)
	}
}

// Stats returns affinity metadata safe for API exposure.
func (s *AffinityStore) Stats() (boundCount int, fileBase string) {
	if s == nil {
		return 0, ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries), filepath.Base(s.path)
}

// BoundCountForPath returns how many conversations are bound to accountPath.
func (s *AffinityStore) BoundCountForPath(accountPath string) int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, rec := range s.entries {
		if rec.AccountPath == accountPath {
			n++
		}
	}
	return n
}
