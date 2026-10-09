package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const refreshLockStaleAfter = 5 * time.Minute

// lockToken serializes token changes within and across processes. Waiting is
// cancellable so abandoned requests do not queue behind another refresh.
func (m *Manager) lockToken(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.refreshLocks == nil {
		m.refreshLocks = make(map[string]chan struct{})
	}
	gate := m.refreshLocks[key]
	if gate == nil {
		gate = make(chan struct{}, 1)
		m.refreshLocks[key] = gate
	}
	m.mu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release, err := m.acquireRefreshFileLock(ctx, key)
	if err != nil {
		<-gate
		return nil, err
	}
	return func() { release(); <-gate }, nil
}

func refreshLockKey(token *Token) string {
	if token == nil {
		return "nil"
	}
	if strings.TrimSpace(token.path) != "" {
		// Different config spellings (relative paths or directory symlinks)
		// must identify the same lock for a shared token file.
		absolute, err := filepath.Abs(token.path)
		if err != nil {
			return filepath.Clean(token.path)
		}
		dir := filepath.Dir(absolute)
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		return filepath.Join(dir, filepath.Base(absolute))
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
			return func() {
				// Never remove a successor's lock if this lock was judged
				// stale and replaced while the original refresh was winding
				// down.
				current, readErr := readFileLimited(lockPath, 4096)
				if readErr == nil && bytes.Equal(current, payload) {
					_ = os.Remove(lockPath)
				}
			}, nil
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
	info, statErr := os.Stat(path)
	if statErr != nil {
		return false
	}
	lockTime := info.ModTime()
	raw, err := readFileLimited(path, 4096)
	if err == nil {
		lines := strings.SplitN(string(raw), "\n", 3)
		if len(lines) >= 2 {
			if nanos, parseErr := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64); parseErr == nil {
				payloadTime := time.Unix(0, nanos)
				// A future payload can result from clock rollback or corrupt
				// state. Fall back to file age so it cannot block refresh
				// forever.
				if !payloadTime.After(now) {
					lockTime = payloadTime
				}
			}
		}
	}
	return now.Sub(lockTime) > refreshLockStaleAfter
}
