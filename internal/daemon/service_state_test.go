package daemon

import (
	"errors"
	"strings"
	"testing"
)

const launchctlPrintRunning = `gui/501/com.droid-proxy.agent = {
	active count = 1
	path = /Users/trevor/Library/LaunchAgents/com.droid-proxy.agent.plist
	type = LaunchAgent
	state = running

	program = /Users/trevor/.local/bin/droid-proxy
	arguments = {
		/Users/trevor/.local/bin/droid-proxy
		start
		--foreground
	}

	pid = 93000
	working directory = /Users/trevor/Library/Application Support/droid-proxy
}`

const launchctlPrintNotRunning = `gui/501/com.droid-proxy.agent = {
	active count = 0
	path = /Users/trevor/Library/LaunchAgents/com.droid-proxy.agent.plist
	type = LaunchAgent
	state = not running

	program = /Users/trevor/.local/bin/droid-proxy
}`

func TestLaunchdRuntimeStateFromOutput(t *testing.T) {
	tests := []struct {
		name        string
		out         string
		err         error
		wantPID     int
		wantRunning bool
		wantDetail  string
	}{
		{
			name:        "running with pid",
			out:         launchctlPrintRunning,
			wantPID:     93000,
			wantRunning: true,
			wantDetail:  "running",
		},
		{
			name:        "loaded but not running",
			out:         launchctlPrintNotRunning,
			wantPID:     0,
			wantRunning: false,
			wantDetail:  "not running",
		},
		{
			name:       "service not bootstrapped",
			out:        "Bad request.\nCould not find service \"com.droid-proxy.agent\" in domain for user gui: 501",
			err:        errors.New("exit status 113"),
			wantDetail: "not loaded",
		},
		{
			name:       "query failure",
			out:        "",
			err:        errors.New("exec: launchctl: not found"),
			wantDetail: "launchctl query failed",
		},
		{
			name:       "garbage output",
			out:        "!!!! unparseable",
			wantDetail: "unknown state",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, running, detail := launchdRuntimeStateFromOutput(tt.out, tt.err)
			if pid != tt.wantPID {
				t.Fatalf("pid = %d, want %d", pid, tt.wantPID)
			}
			if running != tt.wantRunning {
				t.Fatalf("running = %v, want %v", running, tt.wantRunning)
			}
			if !strings.Contains(detail, tt.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", detail, tt.wantDetail)
			}
		})
	}
}

func TestSystemdRuntimeStateFromOutput(t *testing.T) {
	tests := []struct {
		name        string
		out         string
		err         error
		wantPID     int
		wantRunning bool
		wantDetail  string
	}{
		{
			name:        "active with main pid",
			out:         "MainPID=1234\nActiveState=active\n",
			wantPID:     1234,
			wantRunning: true,
			wantDetail:  "active",
		},
		{
			name:       "inactive",
			out:        "MainPID=0\nActiveState=inactive\n",
			wantDetail: "inactive",
		},
		{
			name:       "query failure",
			out:        "",
			err:        errors.New("exit status 1"),
			wantDetail: "systemctl query failed",
		},
		{
			name:       "garbage output",
			out:        "not key value",
			wantDetail: "unknown state",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, running, detail := systemdRuntimeStateFromOutput(tt.out, tt.err)
			if pid != tt.wantPID {
				t.Fatalf("pid = %d, want %d", pid, tt.wantPID)
			}
			if running != tt.wantRunning {
				t.Fatalf("running = %v, want %v", running, tt.wantRunning)
			}
			if !strings.Contains(detail, tt.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", detail, tt.wantDetail)
			}
		})
	}
}

func TestStateDirHonorsHomeAtCallTime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := StateDir(); !strings.HasPrefix(got, home) {
		t.Fatalf("StateDir() = %q, want under %q (must resolve HOME at call time, not package init)", got, home)
	}
	if got := PIDFile(); !strings.HasPrefix(got, home) {
		t.Fatalf("PIDFile() = %q, want under %q", got, home)
	}
}

func TestLaunchdRuntimeStateUsesSeam(t *testing.T) {
	origPrint := launchctlPrintService
	t.Cleanup(func() { launchctlPrintService = origPrint })
	launchctlPrintService = func(label string) (string, error) {
		if label != launchdLabel {
			t.Fatalf("label = %q, want %q", label, launchdLabel)
		}
		return launchctlPrintRunning, nil
	}

	pid, running, detail := launchdQueryRuntime()
	if !running || pid != 93000 {
		t.Fatalf("pid=%d running=%v detail=%q", pid, running, detail)
	}
}

func TestStopLaunchdBootsOutWithoutRemovingPlist(t *testing.T) {
	origBootout := launchctlBootout
	t.Cleanup(func() { launchctlBootout = origBootout })
	var gotDomain, gotPath string
	launchctlBootout = func(domain, path string) error {
		gotDomain, gotPath = domain, path
		return nil
	}

	if err := stopLaunchdService(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotDomain, "gui/") {
		t.Fatalf("domain = %q, want gui/<uid>", gotDomain)
	}
	if !strings.HasSuffix(gotPath, launchdLabel+".plist") {
		t.Fatalf("path = %q, want the %s plist", gotPath, launchdLabel)
	}
}

func TestStopSystemdUserStopsUnit(t *testing.T) {
	origRunner := systemctlRunner
	t.Cleanup(func() { systemctlRunner = origRunner })
	var got []string
	systemctlRunner = func(args ...string) error {
		got = args
		return nil
	}

	if err := stopSystemdUserService(); err != nil {
		t.Fatal(err)
	}
	want := "stop " + systemdUnitName
	if strings.Join(got, " ") != want {
		t.Fatalf("systemctl args = %v, want %q", got, want)
	}
}

func TestSystemdRuntimeStateUsesSeam(t *testing.T) {
	origQuery := systemctlQuery
	t.Cleanup(func() { systemctlQuery = origQuery })
	systemctlQuery = func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, systemdUnitName) || !strings.Contains(joined, "MainPID") {
			t.Fatalf("unexpected systemctl args: %v", args)
		}
		return "MainPID=4321\nActiveState=active\n", nil
	}

	pid, running, detail := systemdQueryRuntime()
	if !running || pid != 4321 {
		t.Fatalf("pid=%d running=%v detail=%q", pid, running, detail)
	}
}
