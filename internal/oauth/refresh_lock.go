package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const refreshLockStaleAfter = 5 * time.Minute

func (m *Manager) refreshMutex(key string) *sync.Mutex {
	if m == nil {
		return &sync.Mutex{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocks == nil {
		m.refreshLocks = make(map[string]*sync.Mutex)
	}
	mu := m.refreshLocks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		m.refreshLocks[key] = mu
	}
	return mu
}

func refreshLockKey(token *Token) string {
	if token == nil {
		return "nil"
	}
	if strings.TrimSpace(token.path) != "" {
		return token.path
	}
	parts := []string{string(token.Provider()), token.Email, token.Subject, token.AccountID}
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			return strings.Join(parts, ":")
		}
	}
	return string(token.Provider()) + ":" + token.RefreshToken
}

func (m *Manager) acquireRefreshFileLock(ctx context.Context, key string) (func(), error) {
	dir, err := m.AuthDir()
	if err != nil {
		return nil, fmt.Errorf("resolve auth dir: %w", err)
	}
	lockDir := filepath.Join(dir, ".locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create auth lock dir: %w", err)
	}
	if err := chmodSecure(lockDir, 0o700, "auth lock dir"); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(lockDir, "refresh-"+refreshLockName(key)+".lock")
	payload := []byte(strconv.Itoa(os.Getpid()) + "\n" + strconv.FormatInt(time.Now().UnixNano(), 10) + "\n")
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, writeErr := f.Write(payload); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("write refresh lock: %w", writeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("close refresh lock: %w", closeErr)
			}
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create refresh lock: %w", err)
		}
		if refreshLockIsStale(lockPath, time.Now()) {
			_ = os.Remove(lockPath)
			continue
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func refreshLockName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

func refreshLockIsStale(path string, now time.Time) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.SplitN(string(raw), "\n", 3)
	if len(lines) < 2 {
		return false
	}
	nanos, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return false
	}
	return now.Sub(time.Unix(0, nanos)) > refreshLockStaleAfter
}
