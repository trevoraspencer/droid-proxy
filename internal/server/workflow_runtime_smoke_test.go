package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestWorkflowValidation_RuntimeSmokeBinaryStartReadinessShutdownCleanup(t *testing.T) {
	t.Setenv("WORKFLOW_VALIDATION_OPENAI_KEY", "sentinel-openai-key")
	t.Setenv("WORKFLOW_VALIDATION_ANTHROPIC_KEY", "sentinel-anthropic-key")
	fake := newValidationFakeUpstream(t)
	bin := filepath.Join(t.TempDir(), "droid-proxy-smoke")
	build := exec.Command("go", "build", "-o", bin, "./cmd/droid-proxy")
	build.Dir = repoRootFromServerTest(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	validConfig := filepath.Join(t.TempDir(), "runtime-smoke.yaml")
	exampleDerivedConfig := exampleDerivedRuntimeSmokeConfigYAML(t, fake.URL())
	loaded, err := configFromYAMLForWorkflow([]byte(exampleDerivedConfig))
	if err != nil {
		t.Fatalf("example-derived runtime smoke config must load: %v", err)
	}
	assertLoopbackOnlyValidationUpstreams(t, loaded)
	if err := os.WriteFile(validConfig, []byte(exampleDerivedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--config", validConfig)
	logs := &lockedBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-done
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	var healthBody, modelsBody string
	var smokeAddr string
	for time.Now().Before(deadline) {
		if smokeAddr == "" {
			smokeAddr = runtimeSmokeListenAddr(logs.String())
		}
		if smokeAddr == "" {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		resp, err := http.Get("http://" + smokeAddr + "/health")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthBody = string(b)
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if healthBody == "" || !strings.Contains(healthBody, `"status":"ok"`) {
		t.Fatalf("binary pid=%d did not become ready; logs=%s", pid, logs.String())
	}
	baseURL := "http://" + smokeAddr
	for _, path := range []string{"/healthz", "/v1/models", "/models"} {
		resp, err := http.Get(baseURL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, resp.StatusCode, b)
		}
		if strings.Contains(path, "models") {
			modelsBody = string(b)
		}
	}
	if !strings.Contains(modelsBody, `"id":"deepseek-v4-flash"`) {
		t.Fatalf("models response missing example-derived alias: %s", modelsBody)
	}
	smokeReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"first run"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(smokeReq)
	if err != nil {
		t.Fatalf("example-derived Droid-facing request failed: %v", err)
	}
	smokeBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(smokeBody), "chat ok") {
		t.Fatalf("example-derived Droid-facing request status=%d body=%s", resp.StatusCode, smokeBody)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal pid=%d: %v", pid, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("binary did not shut down cleanly after SIGTERM: %v logs=%s", err, logs.String())
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("binary pid=%d did not exit within shutdown deadline; logs=%s", pid, logs.String())
	}
	waitForPortFree(t, smokeAddr, 2*time.Second)

	invalidConfig := filepath.Join(t.TempDir(), "invalid-nonloopback.yaml")
	if err := os.WriteFile(invalidConfig, []byte(strings.Replace(workflowValidationConfigYAML(fake.URL(), 0), "host: 127.0.0.1", "host: 0.0.0.0", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := exec.Command(bin, "--config", invalidConfig)
	out, err := invalid.CombinedOutput()
	if err == nil {
		t.Fatalf("invalid startup unexpectedly succeeded: %s", out)
	}
	if !strings.Contains(string(out), "listen.host") || !strings.Contains(string(out), "client_auth.enabled") {
		t.Fatalf("invalid startup error missing clear config guard: %s", out)
	}
}

func exampleDerivedRuntimeSmokeConfigYAML(t *testing.T, upstreamURL string) string {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "sentinel-deepseek-key")
	raw, err := os.ReadFile(filepath.Join(repoRootFromServerTest(t), "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	text = strings.Replace(text, "  port: 9787", "  port: 0", 1)
	text = strings.Replace(text, `base_url: "https://api.deepseek.com/v1"`, `base_url: `+upstreamURL, 1)
	if strings.Contains(text, "known_auth: deepseek") {
		text = strings.Replace(text, "    known_auth: deepseek", fmt.Sprintf("    base_url: %s\n    api_key_env: DEEPSEEK_API_KEY", upstreamURL), 1)
	}
	return text
}

var runtimeSmokeAddrRE = regexp.MustCompile(`addr="?([^"\s]+)"?`)

func runtimeSmokeListenAddr(logs string) string {
	matches := runtimeSmokeAddrRE.FindStringSubmatch(logs)
	if len(matches) != 2 || strings.HasSuffix(matches[1], ":0") {
		return ""
	}
	return matches[1]
}

func waitForPortFree(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener still present on %s after cleanup deadline", addr)
}
