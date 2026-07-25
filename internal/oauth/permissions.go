package oauth

import (
	"fmt"
	"os"
)

var chmodPath = os.Chmod

func chmodSecure(path string, mode os.FileMode, label string) error {
	if err := chmodPath(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", label, err)
	}
	return nil
}
