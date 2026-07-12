package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// RuntimeState describes what the OS service manager reports about the
// droid-proxy service, independent of the pidfile-based daemon state.
type RuntimeState struct {
	Supported bool   // darwin/linux service managers only
	Installed bool   // plist/unit file exists
	Running   bool   // service manager reports a live main process
	PID       int    // main process id when Running
	Detail    string // raw state or query error, for diagnostics
}

// ServiceRunning queries launchctl/systemctl for the live state of the
// installed service. It never consults the pidfile.
func ServiceRunning() RuntimeState {
	switch runtime.GOOS {
	case "darwin":
		return LaunchdRuntimeState()
	case "linux":
		return SystemdRuntimeState()
	default:
		return RuntimeState{Detail: "service state is not supported on " + runtime.GOOS}
	}
}

// StopService stops the installed service through the service manager so it
// stays stopped (plain SIGTERM would be undone by KeepAlive/Restart=always).
func StopService() error {
	switch runtime.GOOS {
	case "darwin":
		return StopLaunchd()
	case "linux":
		return StopSystemdUser()
	default:
		return fmt.Errorf("service stop is not supported on %s", runtime.GOOS)
	}
}

var launchctlBootout = func(domain, path string) error {
	if out, err := exec.Command("launchctl", "bootout", domain, path).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("launchctl", "unload", path).CombinedOutput(); err2 != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = strings.TrimSpace(string(out2))
			}
			return fmt.Errorf("launchctl bootout/unload: %s: %w", msg, err2)
		}
	}
	return nil
}

// StopLaunchd unloads the launch agent so KeepAlive cannot resurrect it. The
// plist stays installed; the agent loads again at next login or via
// droid-proxy restart.
func StopLaunchd() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchd stop is supported on macOS only")
	}
	if !LaunchdInstalled() {
		return fmt.Errorf("launchd service not installed")
	}
	return stopLaunchdService()
}

func stopLaunchdService() error {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	return launchctlBootout(domain, plistPath())
}

// StopSystemdUser stops the systemd user unit (manual stops are honored
// despite Restart=always). The unit stays enabled for the next boot.
func StopSystemdUser() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd user stop is supported on Linux only")
	}
	if !SystemdUserInstalled() {
		return fmt.Errorf("systemd user service not installed")
	}
	return stopSystemdUserService()
}

func stopSystemdUserService() error {
	return systemctlRunner("stop", systemdUnitName)
}

var launchctlPrintService = func(label string) (string, error) {
	uid := os.Getuid()
	target := "gui/" + strconv.Itoa(uid) + "/" + label
	out, err := exec.Command("launchctl", "print", target).CombinedOutput()
	return string(out), err
}

func LaunchdRuntimeState() RuntimeState {
	st := RuntimeState{Supported: runtime.GOOS == "darwin"}
	if !st.Supported {
		st.Detail = "launchd is available on macOS only"
		return st
	}
	st.Installed = LaunchdInstalled()
	if !st.Installed {
		st.Detail = "launchd service not installed"
		return st
	}
	st.PID, st.Running, st.Detail = launchdQueryRuntime()
	return st
}

func launchdQueryRuntime() (pid int, running bool, detail string) {
	out, err := launchctlPrintService(launchdLabel)
	return launchdRuntimeStateFromOutput(out, err)
}

func launchdRuntimeStateFromOutput(out string, err error) (pid int, running bool, detail string) {
	if err != nil {
		if strings.Contains(out, "Could not find service") {
			return 0, false, "not loaded (bootstrap it with droid-proxy service install)"
		}
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return 0, false, "launchctl query failed: " + firstLine(msg)
	}
	// Only the first occurrence of each key counts: launchctl print nests
	// sub-blocks (endpoints, XPC services) whose own "state = active" lines
	// would otherwise override the service-level state.
	state := ""
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := splitStateLine(line)
		if !ok {
			continue
		}
		switch key {
		case "pid":
			if pid == 0 {
				if n, convErr := strconv.Atoi(value); convErr == nil {
					pid = n
				}
			}
		case "state":
			if state == "" {
				state = value
			}
		}
	}
	switch {
	case state == "running":
		return pid, true, "running"
	case state != "":
		return 0, false, state
	default:
		return 0, false, "unknown state"
	}
}

var systemctlQuery = func(args ...string) (string, error) {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	return string(out), err
}

func SystemdRuntimeState() RuntimeState {
	st := RuntimeState{Supported: runtime.GOOS == "linux"}
	if !st.Supported {
		st.Detail = "systemd is available on Linux only"
		return st
	}
	st.Installed = SystemdUserInstalled()
	if !st.Installed {
		st.Detail = "systemd user service not installed"
		return st
	}
	st.PID, st.Running, st.Detail = systemdQueryRuntime()
	return st
}

func systemdQueryRuntime() (pid int, running bool, detail string) {
	out, err := systemctlQuery("--user", "show", systemdUnitName, "--property=MainPID,ActiveState")
	return systemdRuntimeStateFromOutput(out, err)
}

func systemdRuntimeStateFromOutput(out string, err error) (pid int, running bool, detail string) {
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return 0, false, "systemctl query failed: " + firstLine(msg)
	}
	active := ""
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "MainPID":
			if n, convErr := strconv.Atoi(strings.TrimSpace(value)); convErr == nil {
				pid = n
			}
		case "ActiveState":
			active = strings.TrimSpace(value)
		}
	}
	switch {
	case active == "active" && pid > 0:
		return pid, true, "active"
	case active != "":
		return 0, false, active
	default:
		return 0, false, "unknown state"
	}
}

// splitStateLine parses launchctl print body lines of the form "key = value".
func splitStateLine(line string) (key, value string, ok bool) {
	key, value, ok = strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" || strings.HasSuffix(value, "{") {
		return "", "", false
	}
	return key, value, true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
