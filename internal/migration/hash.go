package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// sha256Hex returns the hex-encoded SHA-256 hash of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// shortHash returns a 6-character hex hash of data, suitable for transaction
// ID disambiguation.
func shortHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:3])
}

// hashFile reads and returns the SHA-256 hash of the file at path.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

// formatMode formats an os.FileMode as an octal string like "0640".
func formatMode(m uint32) string {
	return fmt.Sprintf("0%o", m)
}
