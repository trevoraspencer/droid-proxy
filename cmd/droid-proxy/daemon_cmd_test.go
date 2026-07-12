package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
)

func TestWriteStatusMatrix(t *testing.T) {
	tests := []struct {
		name       string
		pid        int
		pidRunning bool
		state      daemon.RuntimeState
		want       []string
		wantAbsent []string
	}{
		{
			name:       "pidfile daemon running, no service",
			pid:        4242,
			pidRunning: true,
			state:      daemon.RuntimeState{Supported: true},
			want:       []string{"droid-proxy is running (pid 4242)"},
			wantAbsent: []string{"launchd"},
		},
		{
			name:       "pidfile running under installed service",
			pid:        4242,
			pidRunning: true,
			state:      daemon.RuntimeState{Supported: true, Installed: true, Running: true, PID: 4242, Detail: "running"},
			want:       []string{"droid-proxy is running (pid 4242)", "managed service: running (pid 4242)"},
		},
		{
			name:  "stale pidfile but service running (incident case)",
			state: daemon.RuntimeState{Supported: true, Installed: true, Running: true, PID: 93000, Detail: "running"},
			want: []string{
				"droid-proxy is running under the managed service (pid 93000)",
				"pidfile state is stale",
			},
			wantAbsent: []string{"droid-proxy is not running."},
		},
		{
			name:  "service installed but inactive",
			state: daemon.RuntimeState{Supported: true, Installed: true, Detail: "not loaded"},
			want: []string{
				"droid-proxy is not running.",
				"service installed but not active (not loaded)",
				"droid-proxy logs",
			},
		},
		{
			name:       "nothing running, no service",
			state:      daemon.RuntimeState{Supported: true},
			want:       []string{"droid-proxy is not running."},
			wantAbsent: []string{"service"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeStatus(&buf,
				func() (int, bool) { return tt.pid, tt.pidRunning },
				func() daemon.RuntimeState { return tt.state },
			)
			out := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("output missing %q:\n%s", want, out)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(out, absent) {
					t.Fatalf("output should not contain %q:\n%s", absent, out)
				}
			}
		})
	}
}

func TestRunStopPrefersServiceManager(t *testing.T) {
	origInstalled, origStopService, origStopDaemon := stopServiceInstalled, stopService, stopDaemon
	t.Cleanup(func() {
		stopServiceInstalled, stopService, stopDaemon = origInstalled, origStopService, origStopDaemon
	})

	serviceStopped := false
	daemonStopped := false
	stopServiceInstalled = func() bool { return true }
	stopService = func() error { serviceStopped = true; return nil }
	stopDaemon = func() error { daemonStopped = true; return nil }

	var buf bytes.Buffer
	if err := stopProxy(&buf); err != nil {
		t.Fatal(err)
	}
	if !serviceStopped {
		t.Fatal("expected StopService to be called when the service is installed")
	}
	if daemonStopped {
		t.Fatal("daemon.Stop should not be called when the service manager handles the stop")
	}
	out := buf.String()
	for _, want := range []string{"stopped managed service", "service uninstall"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunStopFallsBackToDaemon(t *testing.T) {
	origInstalled, origStopService, origStopDaemon := stopServiceInstalled, stopService, stopDaemon
	t.Cleanup(func() {
		stopServiceInstalled, stopService, stopDaemon = origInstalled, origStopService, origStopDaemon
	})

	daemonStopped := false
	stopServiceInstalled = func() bool { return false }
	stopService = func() error { t.Fatal("StopService must not be called"); return nil }
	stopDaemon = func() error { daemonStopped = true; return nil }

	var buf bytes.Buffer
	if err := stopProxy(&buf); err != nil {
		t.Fatal(err)
	}
	if !daemonStopped {
		t.Fatal("expected daemon.Stop fallback")
	}
	if !strings.Contains(buf.String(), "droid-proxy stopped.") {
		t.Fatalf("output missing plain stop message:\n%s", buf.String())
	}
}

func TestRunStopPropagatesServiceError(t *testing.T) {
	origInstalled, origStopService, origStopDaemon := stopServiceInstalled, stopService, stopDaemon
	t.Cleanup(func() {
		stopServiceInstalled, stopService, stopDaemon = origInstalled, origStopService, origStopDaemon
	})

	stopServiceInstalled = func() bool { return true }
	stopService = func() error { return errors.New("bootout failed") }
	stopDaemon = func() error { t.Fatal("daemon.Stop must not be called"); return nil }

	var buf bytes.Buffer
	if err := stopProxy(&buf); err == nil || !strings.Contains(err.Error(), "bootout failed") {
		t.Fatalf("err = %v, want bootout failure", err)
	}
}
