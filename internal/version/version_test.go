package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func withVersionState(t *testing.T, versionValue, commit string, buildInfo *debug.BuildInfo) {
	t.Helper()
	oldVersion, oldCommit, oldReadBuildInfo := Version, Commit, readBuildInfo
	Version, Commit = versionValue, commit
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		if buildInfo == nil {
			return nil, false
		}
		return buildInfo, true
	}
	t.Cleanup(func() {
		Version, Commit, readBuildInfo = oldVersion, oldCommit, oldReadBuildInfo
	})
}

func TestCurrentUsesBuildInfoVCSRevisionWhenCommitUnknown(t *testing.T) {
	withVersionState(t, "", "", &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123def456"},
			{Key: "vcs.modified", Value: "true"},
		},
	})

	info := Current()
	if info.Version != defaultVersion {
		t.Fatalf("Version = %q, want %q", info.Version, defaultVersion)
	}
	if info.Commit != "abc123def456" {
		t.Fatalf("Commit = %q, want build-info revision", info.Commit)
	}
	if !info.Modified {
		t.Fatal("Modified = false, want true")
	}
}

func TestCurrentPrefersExplicitLDFlags(t *testing.T) {
	withVersionState(t, "v1.2.3", "override456", &debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "buildinfo123"},
		},
	})

	info := Current()
	if info.Version != "v1.2.3" || info.Commit != "override456" {
		t.Fatalf("Current() = %#v, want explicit ldflag values", info)
	}
	if got := String(); got != "droid-proxy v1.2.3 (override456)" {
		t.Fatalf("String() = %q", got)
	}
}

func TestProductVersionNeverEmpty(t *testing.T) {
	withVersionState(t, "", "", nil)

	if got := ProductVersion(); got == "" {
		t.Fatal("ProductVersion() is empty")
	}
}

func TestLDFlagsUsesVersionPackagePath(t *testing.T) {
	got := LDFlags("v1.2.3", "abc123")

	for _, want := range []string{
		"-X " + PackagePath + ".Version=v1.2.3",
		"-X " + PackagePath + ".Commit=abc123",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("LDFlags() = %q, want %q", got, want)
		}
	}
}
