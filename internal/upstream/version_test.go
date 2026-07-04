package upstream

import (
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/version"
)

func TestVersionStringNeverEmpty(t *testing.T) {
	oldVersion := version.Version
	version.Version = ""
	t.Cleanup(func() { version.Version = oldVersion })

	if got := versionString(); strings.TrimSpace(got) == "" {
		t.Fatal("versionString() is empty")
	}
}
