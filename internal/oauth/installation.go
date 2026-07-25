package oauth

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) InstallationID() (string, error) {
	dir, err := m.AuthDir()
	if err != nil {
		return "", fmt.Errorf("resolve auth dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}
	if err := chmodSecure(dir, 0o700, "auth dir"); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "installation_id")
	if raw, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(raw)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read installation id: %w", err)
	}
	id, err := randomUUID()
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := chmodSecure(path, 0o600, "installation id"); err != nil {
		return "", err
	}
	return id, nil
}

func randomUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
