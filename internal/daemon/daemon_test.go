package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPIDRoundTrip(t *testing.T) {
	withTempStateDir(t)

	if err := WritePID(); err != nil {
		t.Fatalf("WritePID: %v", err)
	}
	if err := WriteRuntimeMetadata(RuntimeMetadata{PID: os.Getpid(), Executable: os.Args[0]}); err != nil {
		t.Fatalf("WriteRuntimeMetadata: %v", err)
	}
	withProcessHooks(t,
		func(pid int) bool { return pid == os.Getpid() },
		func(pid int, expected string) processIdentity { return processIdentityMatch },
		nil,
	)
	pid, running := IsRunning()
	if !running {
		t.Fatal("expected running after WritePID")
	}
	if pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", pid, os.Getpid())
	}
	RemovePID()
	if _, running := IsRunning(); running {
		t.Fatal("expected not running after RemovePID")
	}
}

func TestCleanStalePIDRemovesLivePIDWithMismatchedIdentity(t *testing.T) {
	withTempStateDir(t)
	writePIDForTest(t, 4242)
	if err := WriteRuntimeMetadata(RuntimeMetadata{PID: 4242, Executable: "/tmp/droid-proxy"}); err != nil {
		t.Fatalf("WriteRuntimeMetadata: %v", err)
	}
	withProcessHooks(t,
		func(pid int) bool { return pid == 4242 },
		func(pid int, expected string) processIdentity { return processIdentityMismatch },
		nil,
	)

	if !CleanStalePID() {
		t.Fatal("expected mismatched live PID to be cleaned as stale")
	}
	if _, err := os.Stat(PIDFile()); !os.IsNotExist(err) {
		t.Fatalf("PID file still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(RuntimeFile()); !os.IsNotExist(err) {
		t.Fatalf("runtime metadata still exists or stat failed: %v", err)
	}
}

func TestStopWithTimeoutRefusesUnknownPIDIdentity(t *testing.T) {
	withTempStateDir(t)
	writePIDForTest(t, 4242)
	if err := WriteRuntimeMetadata(RuntimeMetadata{PID: 4242, Executable: "/tmp/droid-proxy"}); err != nil {
		t.Fatalf("WriteRuntimeMetadata: %v", err)
	}
	signaled := false
	withProcessHooks(t,
		func(pid int) bool { return pid == 4242 },
		func(pid int, expected string) processIdentity { return processIdentityUnknown },
		func(pid int) (processSignaler, error) {
			return signalFunc(func(os.Signal) error {
				signaled = true
				return nil
			}), nil
		},
	)

	err := StopWithTimeout(10 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected not-running error for unknown identity, got %v", err)
	}
	if signaled {
		t.Fatal("StopWithTimeout signaled a PID with unknown identity")
	}
	if _, err := os.Stat(PIDFile()); err != nil {
		t.Fatalf("PID file should remain for unknown identity: %v", err)
	}
}

func TestStopWithTimeoutSignalsVerifiedDaemonPID(t *testing.T) {
	withTempStateDir(t)
	writePIDForTest(t, 4242)
	if err := WriteRuntimeMetadata(RuntimeMetadata{PID: 4242, Executable: "/tmp/droid-proxy"}); err != nil {
		t.Fatalf("WriteRuntimeMetadata: %v", err)
	}
	alive := true
	withProcessHooks(t,
		func(pid int) bool { return pid == 4242 && alive },
		func(pid int, expected string) processIdentity { return processIdentityMatch },
		func(pid int) (processSignaler, error) {
			return signalFunc(func(os.Signal) error {
				alive = false
				return nil
			}), nil
		},
	)

	if err := StopWithTimeout(200 * time.Millisecond); err != nil {
		t.Fatalf("StopWithTimeout: %v", err)
	}
	if _, err := os.Stat(PIDFile()); !os.IsNotExist(err) {
		t.Fatalf("PID file still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(RuntimeFile()); !os.IsNotExist(err) {
		t.Fatalf("runtime metadata still exists or stat failed: %v", err)
	}
}

func TestDeletedProcExecutableStillMatchesExpectedPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "droid-proxy")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	actual := trimDeletedExecutableSuffix(exe + " (deleted)")
	if got := compareExecutableIdentity(actual, exe); got != processIdentityMatch {
		t.Fatalf("compareExecutableIdentity(deleted proc path) = %v, want match", got)
	}
}

type signalFunc func(os.Signal) error

func (f signalFunc) Signal(sig os.Signal) error {
	return f(sig)
}

func writePIDForTest(t *testing.T, pid int) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func withProcessHooks(t *testing.T, alive func(int) bool, verify func(int, string) processIdentity, find func(int) (processSignaler, error)) {
	t.Helper()
	oldAlive := processAlive
	oldVerify := verifyProcessExecutable
	oldFind := findProcess
	if alive != nil {
		processAlive = alive
	}
	if verify != nil {
		verifyProcessExecutable = verify
	}
	if find != nil {
		findProcess = find
	}
	t.Cleanup(func() {
		processAlive = oldAlive
		verifyProcessExecutable = oldVerify
		findProcess = oldFind
	})
}
