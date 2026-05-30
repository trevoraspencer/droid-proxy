package daemon

import (
	"fmt"
	"os"
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
	if !processAlive(pid) {
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
	if !processAlive(pid) {
		RemovePID()
		RemoveRuntimeMetadata()
		return 0, false
	}
	return pid, true
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
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
	proc, err := os.FindProcess(pid)
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
