package tui

import (
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
)

func stubServiceHooks(t *testing.T, installed bool, state daemon.RuntimeState, pidRunning bool) *int {
	t.Helper()
	origInstalled, origRestart, origState, origIsRunning := serviceInstalled, restartService, serviceRunning, daemonIsRunning
	t.Cleanup(func() {
		serviceInstalled, restartService, serviceRunning, daemonIsRunning = origInstalled, origRestart, origState, origIsRunning
	})
	restartCalls := 0
	serviceInstalled = func() bool { return installed }
	restartService = func() error { restartCalls++; return nil }
	serviceRunning = func() daemon.RuntimeState { return state }
	daemonIsRunning = func() (int, bool) {
		if pidRunning {
			return 4242, true
		}
		return 0, false
	}
	return &restartCalls
}

func TestRestartProxyUsesServiceManagerWhenInstalled(t *testing.T) {
	calls := stubServiceHooks(t, true, daemon.RuntimeState{Supported: true, Installed: true, Running: true, PID: 93000, Detail: "running"}, false)

	b := &backend{configPath: "/tmp/config.yaml"}
	if err := b.restartProxy(); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("RestartService calls = %d, want 1 (must not stop+spawn against a managed service)", *calls)
	}
}

func TestProxyRunningSeesManagedService(t *testing.T) {
	stubServiceHooks(t, true, daemon.RuntimeState{Supported: true, Installed: true, Running: true, PID: 93000}, false)

	b := &backend{}
	if !b.proxyRunning() {
		t.Fatal("proxyRunning must report true when the managed service is running, even with a stale pidfile")
	}
}

func TestRestartHintOnlyWhenProxyRunning(t *testing.T) {
	stubServiceHooks(t, false, daemon.RuntimeState{}, true)
	b := &backend{}
	hint := b.restartHint()
	if !strings.Contains(hint, "estart") || !strings.Contains(hint, "apply") {
		t.Fatalf("restartHint = %q, want a restart-to-apply nudge", hint)
	}

	stubServiceHooks(t, false, daemon.RuntimeState{}, false)
	if got := b.restartHint(); got != "" {
		t.Fatalf("restartHint = %q, want empty when nothing is running", got)
	}
}
