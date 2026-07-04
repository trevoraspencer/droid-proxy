package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

const (
	defaultVersion = "0.0.0-dev"
	unknownCommit  = "unknown"
	PackagePath    = "github.com/trevoraspencer/droid-proxy/internal/version"
)

var (
	Version string
	Commit  string

	readBuildInfo = debug.ReadBuildInfo
)

type Info struct {
	Version  string
	Commit   string
	Modified bool
}

func Current() Info {
	info := Info{
		Version: strings.TrimSpace(Version),
		Commit:  strings.TrimSpace(Commit),
	}
	if bi, ok := readBuildInfo(); ok && bi != nil {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, setting := range bi.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" && strings.TrimSpace(setting.Value) != "" {
					info.Commit = strings.TrimSpace(setting.Value)
				}
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}
	info.Version = nonEmpty(info.Version, defaultVersion)
	info.Commit = nonEmpty(info.Commit, unknownCommit)
	return info
}

func String() string {
	info := Current()
	return fmt.Sprintf("droid-proxy %s (%s)", info.Version, info.Commit)
}

func ProductVersion() string {
	return Current().Version
}

func LDFlags(versionValue, commit string) string {
	if strings.TrimSpace(versionValue) == "" {
		versionValue = defaultVersion
	}
	if strings.TrimSpace(commit) == "" {
		commit = unknownCommit
	}
	return fmt.Sprintf("-X %s.Version=%s -X %s.Commit=%s", PackagePath, versionValue, PackagePath, commit)
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
