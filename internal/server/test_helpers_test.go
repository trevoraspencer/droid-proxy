package server

import (
	"os"
)

func testWriteFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o600)
}
