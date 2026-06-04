package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const dirName = ".droid-proxy"

var (
	stateDir = filepath.Join(os.Getenv("HOME"), dirName)
	pidFile  = filepath.Join(stateDir, "droid-proxy.pid")

	processAlive            = processAliveDefault
	findProcess             = findProcessDefault
	verifyProcessExecutable = verifyProcessExecutableDefault
)

// StateDir returns ~/.droid-proxy (created on demand by callers).
func StateDir() string { return stateDir }

// PIDFile returns the path to the daemon PID file.
func PIDFile() string { return pidFile }

// WritePID atomically writes the current process PID.
func WritePID() error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(pidFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("PID file already exists — another instance may be running (use 'droid-proxy stop' first)")
		}
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d", os.Getpid())
	return err
}

func readPID() (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// RemovePID deletes the PID file if present.
func RemovePID() {
	_ = os.Remove(pidFile)
}

// CleanStalePID removes a PID file if the referenced process is no longer running.
func CleanStalePID() bool {
	pid, err := readPID()
	if err != nil {
		return false
	}
	if stale, _ := daemonPIDState(pid); stale {
		RemovePID()
		RemoveRuntimeMetadata()
		return true
	}
	return false
}

// IsRunning reports whether the daemon PID file refers to a live process.
func IsRunning() (int, bool) {
	pid, err := readPID()
	if err != nil {
		return 0, false
	}
	stale, verified := daemonPIDState(pid)
	if stale {
		RemovePID()
		RemoveRuntimeMetadata()
		return 0, false
	}
	if !verified {
		return 0, false
	}
	return pid, true
}

func processAliveDefault(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

type processSignaler interface {
	Signal(os.Signal) error
}

func findProcessDefault(pid int) (processSignaler, error) {
	return os.FindProcess(pid)
}

type processIdentity int

const (
	processIdentityUnknown processIdentity = iota
	processIdentityMatch
	processIdentityMismatch
)

func daemonPIDState(pid int) (stale bool, verified bool) {
	if !processAlive(pid) {
		return true, false
	}
	meta, err := ReadRuntimeMetadata()
	if err != nil {
		return false, false
	}
	if meta.PID != pid || strings.TrimSpace(meta.Executable) == "" {
		return true, false
	}
	switch verifyProcessExecutable(pid, meta.Executable) {
	case processIdentityMatch:
		return false, true
	case processIdentityMismatch:
		return true, false
	default:
		return false, false
	}
}

func verifyProcessExecutableDefault(pid int, expected string) processIdentity {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return processIdentityUnknown
	}
	if actual, ok := procExecutablePath(pid); ok {
		return compareExecutableIdentity(actual, expected)
	}
	if actual, ok := processCommandLine(pid); ok {
		return compareCommandLineIdentity(actual, expected)
	}
	if actual, ok := processCommandName(pid); ok {
		return compareExecutableIdentity(actual, expected)
	}
	return processIdentityUnknown
}

func procExecutablePath(pid int) (string, bool) {
	path, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	return path, true
}

func processCommandName(pid int) (string, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", false
	}
	name := strings.TrimSpace(string(out))
	return name, name != ""
}

func processCommandLine(pid int) (string, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", false
	}
	command := strings.TrimSpace(string(out))
	return command, command != ""
}

func compareCommandLineIdentity(command, expected string) processIdentity {
	command = strings.TrimSpace(command)
	expected = strings.TrimSpace(expected)
	if command == "" || expected == "" {
		return processIdentityUnknown
	}
	expectedAbs, err := filepath.Abs(expected)
	if err == nil {
		if expectedReal, realErr := filepath.EvalSymlinks(expectedAbs); realErr == nil {
			expectedAbs = expectedReal
		}
		if command == expectedAbs || strings.HasPrefix(command, expectedAbs+" ") {
			return processIdentityMatch
		}
	}
	if command == expected || strings.HasPrefix(command, expected+" ") {
		return processIdentityMatch
	}
	firstArg := strings.Fields(command)
	if len(firstArg) > 0 && filepath.IsAbs(firstArg[0]) {
		return compareExecutableIdentity(firstArg[0], expected)
	}
	return processIdentityUnknown
}

func compareExecutableIdentity(actual, expected string) processIdentity {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if actual == "" || expected == "" {
		return processIdentityUnknown
	}
	if sameExecutablePath(actual, expected) {
		return processIdentityMatch
	}
	if filepath.Base(actual) == filepath.Base(expected) {
		return processIdentityMatch
	}
	return processIdentityMismatch
}

func sameExecutablePath(actual, expected string) bool {
	actualAbs, actualErr := filepath.Abs(actual)
	expectedAbs, expectedErr := filepath.Abs(expected)
	if actualErr != nil || expectedErr != nil {
		return false
	}
	if actualReal, err := filepath.EvalSymlinks(actualAbs); err == nil {
		actualAbs = actualReal
	}
	if expectedReal, err := filepath.EvalSymlinks(expectedAbs); err == nil {
		expectedAbs = expectedReal
	}
	return actualAbs == expectedAbs
}

// Stop sends SIGTERM and waits for the process to exit.
func Stop() error {
	return StopWithTimeout(10 * time.Second)
}

// StopWithTimeout sends SIGTERM and waits up to timeout for the process to exit.
func StopWithTimeout(timeout time.Duration) error {
	pid, running := IsRunning()
	if !running {
		return fmt.Errorf("droid-proxy is not running")
	}
	proc, err := findProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM to pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			RemovePID()
			RemoveRuntimeMetadata()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit within %s", pid, timeout)
}
