package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type ServiceCheck struct {
	Kind             string
	Path             string
	Installed        bool
	ProgramArguments []string
	Issues           []string
}

func ServiceDescription() string {
	switch runtime.GOOS {
	case "darwin":
		return "launchd user agent " + launchdLabel
	case "linux":
		return "systemd user unit " + systemdUnitName
	default:
		return "unsupported service for " + runtime.GOOS
	}
}

func ServiceInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		return LaunchdInstalled()
	case "linux":
		return SystemdUserInstalled()
	default:
		return false
	}
}

func InstallService(configPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return InstallLaunchd(configPath)
	case "linux":
		return InstallSystemdUser(configPath)
	default:
		return fmt.Errorf("service install is not supported on %s", runtime.GOOS)
	}
}

func UninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		return UninstallLaunchd()
	case "linux":
		return UninstallSystemdUser()
	default:
		return fmt.Errorf("service uninstall is not supported on %s", runtime.GOOS)
	}
}

func RestartService() error {
	switch runtime.GOOS {
	case "darwin":
		return RestartLaunchd()
	case "linux":
		return RestartSystemdUser()
	default:
		return fmt.Errorf("service restart is not supported on %s", runtime.GOOS)
	}
}

func CheckService() (ServiceCheck, error) {
	switch runtime.GOOS {
	case "darwin":
		check, err := CheckLaunchdPlist(LaunchdPlistPath())
		return ServiceCheck{
			Kind:             "launchd",
			Path:             check.Path,
			Installed:        check.Installed,
			ProgramArguments: check.ProgramArguments,
			Issues:           check.Issues,
		}, err
	case "linux":
		check, err := CheckSystemdUnit(SystemdUnitPath())
		return ServiceCheck{
			Kind:             "systemd user",
			Path:             check.Path,
			Installed:        check.Installed,
			ProgramArguments: check.ProgramArguments,
			Issues:           check.Issues,
		}, err
	default:
		return ServiceCheck{Kind: runtime.GOOS}, nil
	}
}

func validateServiceConfig(configPath string) (string, string, error) {
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return "", "", fmt.Errorf("config path: %w", err)
	}
	info, err := os.Stat(absConfig)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("config %s does not exist; run droid-proxy config or pass a valid --config", absConfig)
		}
		return "", "", fmt.Errorf("config %s: %w", absConfig, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("config %s is a directory; run droid-proxy config or pass a valid --config", absConfig)
	}
	return absConfig, filepath.Dir(absConfig), nil
}

func serviceArgumentIssues(args []string) []string {
	var issues []string
	if len(args) == 0 {
		return []string{"missing ProgramArguments"}
	}
	issues = append(issues, serviceExecutableIssues(args[0])...)
	for i, arg := range args {
		switch arg {
		case "--config":
			if i+1 >= len(args) {
				issues = append(issues, "missing value after --config")
				continue
			}
			if _, err := os.Stat(args[i+1]); err != nil {
				issues = append(issues, "missing config path: "+args[i+1])
			}
		case "--env-file":
			if i+1 >= len(args) {
				issues = append(issues, "missing value after --env-file")
				continue
			}
			envPath := args[i+1]
			if strings.Contains(filepath.Base(envPath), ".env.live-e2e.local") {
				issues = append(issues, "service should not reference .env.live-e2e.local: "+envPath)
			}
			if _, err := os.Stat(envPath); err != nil {
				issues = append(issues, "missing env file path: "+envPath)
			}
		}
	}
	return issues
}

func serviceExecutableIssues(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return []string{"missing executable path"}
	}
	if _, err := os.Stat(path); err != nil {
		return []string{"missing executable path: " + path}
	}
	if looksLikeSourceCheckoutBinary(path) {
		return []string{"service executable points at source checkout: " + path}
	}
	return nil
}

func looksLikeSourceCheckoutBinary(path string) bool {
	dir := filepath.Dir(path)
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "droid-proxy")); err != nil {
		return false
	}
	return true
}
