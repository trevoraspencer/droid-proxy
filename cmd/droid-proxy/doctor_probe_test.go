package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// TestMain keeps every doctor test hermetic: no test opens real sockets
// unless it explicitly stubs doctorProbeListen itself.
func TestMain(m *testing.M) {
	doctorProbeListen = func(cfg *config.Config) (healthProbe, healthProbe) {
		addr := "127.0.0.1:" + strconv.Itoa(cfg.Listen.Port)
		return healthProbe{Addr: addr, Err: errors.New("probe stubbed in tests")},
			healthProbe{Addr: "[::1]:" + strconv.Itoa(cfg.Listen.Port), Err: errors.New("probe stubbed in tests")}
	}
	os.Exit(m.Run())
}

func stubDoctorProbe(t *testing.T, primary, v6 healthProbe) {
	t.Helper()
	orig := doctorProbeListen
	doctorProbeListen = func(*config.Config) (healthProbe, healthProbe) { return primary, v6 }
	t.Cleanup(func() { doctorProbeListen = orig })
}

func probeClient() *http.Client { return &http.Client{Timeout: time.Second} }

func TestDefaultProbeListenFindsRealServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"droid-proxy","version":"vtest"}`))
	}))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Listen: config.Listen{Host: "127.0.0.1", Port: port}}
	primary, v6 := defaultProbeListen(cfg)
	if !primary.OK || primary.Service != "droid-proxy" {
		t.Fatalf("primary = %+v, want droid-proxy answer", primary)
	}
	if v6.Addr != "[::1]:"+portStr {
		t.Fatalf("v6 probe addr = %q, want [::1]:%s", v6.Addr, portStr)
	}
}

func TestDoctorFlagsForeignListenerAsHardIssue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	rawConfig := `
models:
  - alias: local-tools-off
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: llama
    known_auth: ollama
    capabilities:
      tools: false
`
	if err := os.WriteFile(configPath, []byte(rawConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	withDoctorEnvHooks(t, filepath.Join(tmp, "managed-env"))
	stubDoctorProbe(t,
		healthProbe{Addr: "127.0.0.1:8787", OK: true, Service: "something-else"},
		healthProbe{Addr: "[::1]:8787", Err: errors.New("refused")},
	)

	var out bytes.Buffer
	res := writeDoctorWithOptions(&out, doctorOptions{ConfigPath: configPath, ConfigExplicit: true})
	text := out.String()
	if len(res.HardIssues) == 0 {
		t.Fatalf("want port-conflict hard issue\noutput:\n%s", text)
	}
	if !strings.Contains(text, "another server answered on 127.0.0.1:8787") {
		t.Fatalf("doctor output missing port-conflict issue:\n%s", text)
	}
}

func TestDoctorWarnsAboutIPv6Squatter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	rawConfig := `
models:
  - alias: local-tools-off
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: llama
    known_auth: ollama
    capabilities:
      tools: false
`
	if err := os.WriteFile(configPath, []byte(rawConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	withDoctorEnvHooks(t, filepath.Join(tmp, "managed-env"))
	stubDoctorProbe(t,
		healthProbe{Addr: "127.0.0.1:8787", OK: true, Service: "droid-proxy", Version: "vtest"},
		healthProbe{Addr: "[::1]:8787", OK: true, Service: ""},
	)

	var out bytes.Buffer
	res := writeDoctorWithOptions(&out, doctorOptions{ConfigPath: configPath, ConfigExplicit: true})
	text := out.String()
	if len(res.HardIssues) != 0 {
		t.Fatalf("an IPv6 squatter must be a soft warning; HardIssues = %#v\noutput:\n%s", res.HardIssues, text)
	}
	for _, want := range []string{
		"health probe (127.0.0.1:8787): ok droid-proxy vtest",
		"warning: a different server is listening on [::1]:8787",
		"status: ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestProbeHealthRecognizesDroidProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("path = %q, want /health", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"droid-proxy","version":"v9.9.9","commit":"abc"}`))
	}))
	t.Cleanup(srv.Close)

	p := probeHealth(probeClient(), srv.URL)
	if !p.OK || p.Service != "droid-proxy" || p.Version != "v9.9.9" {
		t.Fatalf("probe = %+v, want ok droid-proxy v9.9.9", p)
	}
}

func TestProbeHealthForeignServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Not found.", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	p := probeHealth(probeClient(), srv.URL)
	if !p.OK {
		t.Fatalf("probe = %+v; a foreign responder still answered — OK must be true with empty Service", p)
	}
	if p.Service == "droid-proxy" {
		t.Fatalf("probe = %+v; foreign server must not be identified as droid-proxy", p)
	}
}

func TestProbeHealthNoListener(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // guaranteed-refused port

	p := probeHealth(probeClient(), url)
	if p.OK || p.Err == nil {
		t.Fatalf("probe = %+v, want connection failure", p)
	}
}

func TestWriteListenProbeRules(t *testing.T) {
	droid := healthProbe{Addr: "127.0.0.1:8787", OK: true, Service: "droid-proxy", Version: "v1"}
	foreign := healthProbe{Addr: "127.0.0.1:8787", OK: true, Service: ""}
	foreignV6 := healthProbe{Addr: "[::1]:8787", OK: true, Service: ""}
	silent := healthProbe{Addr: "127.0.0.1:8787", Err: errors.New("connection refused")}
	silentV6 := healthProbe{Addr: "[::1]:8787", Err: errors.New("connection refused")}

	tests := []struct {
		name        string
		primary, v6 healthProbe
		expectRun   bool
		want        []string
		wantAbsent  []string
		wantHard    bool
	}{
		{
			name:    "healthy droid-proxy, quiet v6",
			primary: droid, v6: silentV6,
			want:       []string{"health probe (127.0.0.1:8787): ok droid-proxy v1"},
			wantAbsent: []string{"[::1]"},
		},
		{
			name:    "foreign server on the configured address is a hard issue",
			primary: foreign, v6: silentV6,
			want:     []string{"health probe: issue:", "another server answered on 127.0.0.1:8787"},
			wantHard: true,
		},
		{
			name:    "no answer while service claims running is a hard issue",
			primary: silent, v6: silentV6,
			expectRun: true,
			want:      []string{"health probe: issue:", "not responding"},
			wantHard:  true,
		},
		{
			name:    "no answer while stopped is soft",
			primary: silent, v6: silentV6,
			want: []string{"health probe (127.0.0.1:8787): not responding"},
		},
		{
			name:    "v6 squatter warns softly",
			primary: droid, v6: foreignV6,
			want: []string{
				"warning: a different server is listening on [::1]:8787",
				"use http://127.0.0.1:8787",
				"Cursor MCP OAuth loopback",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			hard := writeListenProbe(&buf, tt.primary, tt.v6, tt.expectRun)
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
			if tt.wantHard != (len(hard) > 0) {
				t.Fatalf("hard issues = %v, wantHard = %v\noutput:\n%s", hard, tt.wantHard, out)
			}
		})
	}
}
