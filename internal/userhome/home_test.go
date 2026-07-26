package userhome

import (
	"path/filepath"
	"testing"
)

func TestDirDoesNotBecomeRelativeWhenHOMEIsUnset(t *testing.T) {
	t.Setenv("HOME", "")
	if got := Dir(); !filepath.IsAbs(got) {
		t.Fatalf("Dir() = %q, want an absolute fail-closed path", got)
	}
}
