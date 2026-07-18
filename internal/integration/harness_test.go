package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
	"github.com/trevoraspencer/droid-proxy/internal/handlers"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

// ---------------------------------------------------------------------------
// Captured request tracking
// ---------------------------------------------------------------------------

// capturedRequest holds one upstream request's sanitized details.
type capturedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// requestCapture is a thread-safe recorder of upstream requests.
type requestCapture struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (rc *requestCapture) record(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	rc.requests = append(rc.requests, capturedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   b,
	})
	rc.mu.Unlock()
	return b
}

func (rc *requestCapture) Count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.requests)
}

func (rc *requestCapture) Get(i int) capturedRequest {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if i < 0 || i >= len(rc.requests) {
		return capturedRequest{}
	}
	return rc.requests[i]
}

// All returns a snapshot of all captured requests.
func (rc *requestCapture) All() []capturedRequest {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]capturedRequest, len(rc.requests))
	copy(out, rc.requests)
	return out
}

// Reset clears all captured requests.
func (rc *requestCapture) Reset() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.requests = nil
}

// ---------------------------------------------------------------------------
// Fake upstream server
// ---------------------------------------------------------------------------

// fakeUpstream is a local HTTP server that captures requests and serves
// deterministic responses. Each provider gets its own instance so isolation
// can be verified.
type fakeUpstream struct {
	server   *httptest.Server
	capture  *requestCapture
	respBody string
	respCode int
	mu       sync.Mutex
}

// newFakeUpstream creates a local fake OpenAI-compatible server on an
// OS-assigned port. The response body and status can be changed at runtime.
func newFakeUpstream(t *testing.T, name, respBody string) *fakeUpstream {
	t.Helper()
	fu := &fakeUpstream{
		capture:  &requestCapture{},
		respBody: respBody,
		respCode: http.StatusOK,
	}
	fu.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := fu.capture.record(r)
		_ = body
		fu.mu.Lock()
		code := fu.respCode
		resp := fu.respBody
		fu.mu.Unlock()
		if strings.HasPrefix(resp, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(resp))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(fu.server.Close)
	t.Logf("fake upstream %q listening at %s", name, fu.server.URL)
	return fu
}

// SetResponse changes the response body and status code.
func (fu *fakeUpstream) SetResponse(body string, code int) {
	fu.mu.Lock()
	defer fu.mu.Unlock()
	fu.respBody = body
	fu.respCode = code
}

// BaseURL returns the fake server's base URL.
func (fu *fakeUpstream) BaseURL() string {
	return fu.server.URL
}

// Capture returns the request recorder.
func (fu *fakeUpstream) Capture() *requestCapture { return fu.capture }

// ---------------------------------------------------------------------------
// Credential sentinels
// ---------------------------------------------------------------------------

// Synthetic credential sentinels used across integration tests. These are
// never real provider keys.
const (
	sentinelFireworksStd   = "fw_integ_standard_sentinel_001"
	sentinelFireworksPass  = "fpk_integ_firepass_sentinel_002"
	sentinelBaseten        = "baseten_integ_sentinel_003"
	sentinelDeepInfra      = "deepinfra_integ_sentinel_004"
	sentinelCustomEndpoint = "custom_integ_sentinel_005"
)

// clearInheritedCredentials removes any inherited provider credential,
// client-auth, OAuth/session, and Factory authentication variables from the
// test process environment, then injects only synthetic values. This matches
// the validation boundary: no credential source outside the isolated root or
// explicit synthetic environment is read.
func clearInheritedCredentials(t *testing.T) {
	t.Helper()
	// Clear all provider credential env vars that could be inherited.
	for _, env := range []string{
		"FIREWORKS_API_KEY",
		"FIREWORKS_FIRE_PASS_API_KEY",
		"BASETEN_API_KEY",
		"DEEPINFRA_TOKEN",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"DEEPSEEK_API_KEY",
		"XAI_API_KEY",
		"GROQ_API_KEY",
	} {
		t.Setenv(env, "")
	}
	// Clear proxy/auth variables.
	for _, env := range []string{
		"HTTPS_PROXY",
		"HTTP_PROXY",
		"ALL_PROXY",
		"NO_PROXY",
	} {
		t.Setenv(env, "")
	}
}

// injectSyntheticCredentials sets the synthetic sentinel values used by the
// combined installation.
func injectSyntheticCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("FIREWORKS_API_KEY", sentinelFireworksStd)
	t.Setenv("FIREWORKS_FIRE_PASS_API_KEY", sentinelFireworksPass)
	t.Setenv("BASETEN_API_KEY", sentinelBaseten)
	t.Setenv("DEEPINFRA_TOKEN", sentinelDeepInfra)
}

// ---------------------------------------------------------------------------
// Combined installation builder
// ---------------------------------------------------------------------------

// providerModelDef describes one model in the combined installation.
type providerModelDef struct {
	Alias           string
	DisplayName     string
	KnownAuth       string
	UpstreamModel   string
	ExtraArgs       map[string]any
	BaseURLOverride string // when non-empty, overrides the registry BaseURL
}

// combinedInstallation is the harness for one isolated combined setup. It
// writes a config YAML and Factory settings JSON that mirror what the real TUI
// would produce, creates per-provider fake upstreams, and supports reload from
// persisted files before runtime requests.
type combinedInstallation struct {
	t            *testing.T
	home         string
	configPath   string
	factoryPath  string
	envPath      string
	proxyBaseURL string // the fake "proxy" address used in Factory entries

	upstreams map[string]*fakeUpstream // keyed by provider name

	// modelDefs are the definitions used to build the installation.
	modelDefs []providerModelDef
}

// newCombinedInstallation creates an isolated temporary root and writes a
// combined config with all four native providers plus Fireworks variants.
// Each provider points at its own local fake upstream via base_url override.
// The Factory settings JSON is synced with one entry per alias, all pointing
// at proxyBaseURL (an OS-assigned loopback address, never a reserved port).
func newCombinedInstallation(t *testing.T) *combinedInstallation {
	t.Helper()
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	home := t.TempDir()
	ci := &combinedInstallation{
		t:            t,
		home:         home,
		upstreams:    make(map[string]*fakeUpstream),
		proxyBaseURL: "http://127.0.0.1:9787", // expected production default, never bound
	}

	// Create per-provider fake upstreams.
	ci.upstreams["fireworks"] = newFakeUpstream(t, "fireworks",
		`{"id":"fw-resp","choices":[{"index":0,"message":{"role":"assistant","content":"fireworks-ok"}}],"model":"fw-upstream"}`)
	ci.upstreams["baseten"] = newFakeUpstream(t, "baseten",
		`{"id":"bt-resp","choices":[{"index":0,"message":{"role":"assistant","content":"baseten-ok"}}],"model":"bt-upstream"}`)
	ci.upstreams["deepinfra"] = newFakeUpstream(t, "deepinfra",
		`{"id":"di-resp","choices":[{"index":0,"message":{"role":"assistant","content":"deepinfra-ok"}}],"model":"di-upstream"}`)

	// Set up the canonical model definitions. These mirror what the TUI
	// persists after onboarding each provider.
	ci.modelDefs = []providerModelDef{
		// Fireworks Standard: no service_tier
		{
			Alias:           "fw-standard",
			DisplayName:     "Fireworks Standard",
			KnownAuth:       "fireworks",
			UpstreamModel:   "accounts/fireworks/models/glm-4-standard",
			BaseURLOverride: ci.upstreams["fireworks"].BaseURL() + "/inference/v1",
		},
		// Fireworks Priority: service_tier: priority
		{
			Alias:           "fw-priority",
			DisplayName:     "Fireworks Priority",
			KnownAuth:       "fireworks",
			UpstreamModel:   "accounts/fireworks/models/glm-4-priority",
			ExtraArgs:       map[string]any{"service_tier": "priority"},
			BaseURLOverride: ci.upstreams["fireworks"].BaseURL() + "/inference/v1",
		},
		// Fireworks Fast: router model, no service_tier
		{
			Alias:           "fw-fast",
			DisplayName:     "Fireworks Fast",
			KnownAuth:       "fireworks",
			UpstreamModel:   "accounts/fireworks/routers/glm-5p2-fast",
			BaseURLOverride: ci.upstreams["fireworks"].BaseURL() + "/inference/v1",
		},
		// Fire Pass: own profile, own credential, own router
		{
			Alias:           "fw-firepass",
			DisplayName:     "Fireworks Fire Pass",
			KnownAuth:       "fireworks-fire-pass",
			UpstreamModel:   "accounts/fireworks/routers/glm-5p2-fast",
			BaseURLOverride: ci.upstreams["fireworks"].BaseURL() + "/inference/v1",
		},
		// Baseten native
		{
			Alias:           "baseten-model",
			DisplayName:     "Baseten Model",
			KnownAuth:       "baseten",
			UpstreamModel:   "org-team/proj-q3-model",
			BaseURLOverride: ci.upstreams["baseten"].BaseURL() + "/v1",
		},
		// DeepInfra native
		{
			Alias:           "deepinfra-model",
			DisplayName:     "DeepInfra Model",
			KnownAuth:       "deepinfra",
			UpstreamModel:   "meta-llama/Llama-3.3-70B-Instruct",
			BaseURLOverride: ci.upstreams["deepinfra"].BaseURL() + "/v1/openai",
		},
	}

	// Write the config and Factory settings.
	ci.configPath = filepath.Join(home, ".config", "droid-proxy", "config.yaml")
	ci.factoryPath = filepath.Join(home, ".factory", "settings.json")
	ci.envPath = filepath.Join(home, ".droid-proxy", "env")

	ci.writeConfig()
	ci.writeFactorySettings()
	ci.writeManagedEnv()

	// Set HOME so factory.DefaultSettingsPath() resolves to our temp root.
	t.Setenv("HOME", home)

	return ci
}

// writeConfig writes the combined config YAML, mirroring what the TUI
// persists. It uses listen.port: 9787 (the production default) which is never
// bound during tests; all requests go through the handler engine directly or
// through an OS-assigned proxy port.
func (ci *combinedInstallation) writeConfig() {
	ci.t.Helper()
	dir := filepath.Dir(ci.configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		ci.t.Fatal(err)
	}

	doc := map[string]any{
		"listen": map[string]any{
			"host": "127.0.0.1",
			"port": 9787,
		},
	}

	var models []map[string]any
	for _, md := range ci.modelDefs {
		m := map[string]any{
			"alias":             md.Alias,
			"display_name":      md.DisplayName,
			"factory_provider":  "generic-chat-completion-api",
			"upstream_protocol": "openai-chat",
			"known_auth":        md.KnownAuth,
			"upstream_model":    md.UpstreamModel,
		}
		if md.BaseURLOverride != "" {
			m["base_url"] = md.BaseURLOverride
		}
		if len(md.ExtraArgs) > 0 {
			m["extra_args"] = md.ExtraArgs
		}
		models = append(models, m)
	}
	doc["models"] = models

	data, err := yaml.Marshal(doc)
	if err != nil {
		ci.t.Fatal(err)
	}
	if err := os.WriteFile(ci.configPath, data, 0o600); err != nil {
		ci.t.Fatal(err)
	}
}

// writeFactorySettings writes a Factory settings JSON with one customModels
// entry per alias, all pointing at the proxy base URL. This mirrors what TUI
// sync produces.
func (ci *combinedInstallation) writeFactorySettings() {
	ci.t.Helper()
	dir := filepath.Dir(ci.factoryPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		ci.t.Fatal(err)
	}

	entries := make([]map[string]any, 0, len(ci.modelDefs))
	for _, md := range ci.modelDefs {
		entries = append(entries, map[string]any{
			"model":           md.Alias,
			"displayName":     md.DisplayName,
			"provider":        "generic-chat-completion-api",
			"baseUrl":         ci.proxyBaseURL,
			"apiKey":          "x",
			"maxOutputTokens": 128000,
		})
	}

	doc := map[string]any{
		"customModels": entries,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		ci.t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(ci.factoryPath, data, 0o600); err != nil {
		ci.t.Fatal(err)
	}
}

// writeManagedEnv writes the private managed env file with synthetic
// credential assignments.
func (ci *combinedInstallation) writeManagedEnv() {
	ci.t.Helper()
	dir := filepath.Dir(ci.envPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		ci.t.Fatal(err)
	}
	lines := []string{
		"FIREWORKS_API_KEY=" + sentinelFireworksStd,
		"FIREWORKS_FIRE_PASS_API_KEY=" + sentinelFireworksPass,
		"BASETEN_API_KEY=" + sentinelBaseten,
		"DEEPINFRA_TOKEN=" + sentinelDeepInfra,
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	if err := os.WriteFile(ci.envPath, data, 0o600); err != nil {
		ci.t.Fatal(err)
	}
}

// loadFromDisk reloads the config from the persisted YAML file, applying
// defaults and hydration just as the real server does on startup. This
// simulates a restart from persisted files.
func (ci *combinedInstallation) loadFromDisk() *config.Config {
	ci.t.Helper()
	cfg, err := config.Load(ci.configPath)
	if err != nil {
		ci.t.Fatalf("load config from disk: %v", err)
	}
	return cfg
}

// buildAPI constructs a real handler API from the given config, wired to the
// combined installation's fake upstreams. The engine registers Chat and Models
// routes exactly as the real server does.
func (ci *combinedInstallation) buildAPI(cfg *config.Config) (*handlers.API, *gin.Engine) {
	ci.t.Helper()
	gin.SetMode(gin.TestMode)
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		ci.t.Fatalf("new router: %v", err)
	}
	client := upstream.NewClient(cfg)
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	api := handlers.NewAPI(cfg, router, client, nil, nil, logger)
	engine := gin.New()
	engine.POST("/v1/chat/completions", api.ChatCompletions)
	engine.POST("/chat/completions", api.ChatCompletions)
	engine.GET("/v1/models", api.Models)
	engine.GET("/models", api.Models)
	return api, engine
}

// factoryAliases reads the temporary Factory settings JSON and returns the
// list of model aliases registered there. Per VAL-CROSS-004, request models
// are read from the provider's temporary customModels[].model, not duplicated
// as harness constants.
func (ci *combinedInstallation) factoryAliases() []string {
	ci.t.Helper()
	s, err := factory.Load(ci.factoryPath)
	if err != nil {
		ci.t.Fatalf("load factory settings: %v", err)
	}
	models, err := s.Models()
	if err != nil {
		ci.t.Fatalf("factory models: %v", err)
	}
	sort.Strings(models)
	return models
}

// sendChat sends a /v1/chat/completions request with the given alias and
// optional extra JSON body fields merged into the base request.
func sendChat(t *testing.T, engine *gin.Engine, alias string, extraFields string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]`, alias)
	if extraFields != "" {
		body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],%s`, alias, extraFields)
	}
	body += "}"
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w, []byte(body)
}

// sendChatRaw sends an arbitrary JSON body to /v1/chat/completions.
func sendChatRaw(t *testing.T, engine *gin.Engine, jsonBody string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(jsonBody))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

// sha256Hex computes the hex-encoded SHA-256 digest of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// fileHash reads a file and returns its SHA-256 hex digest.
func fileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(data)
}

// assertNoSentinel scans a byte slice for any of the synthetic credential
// sentinels and fails if any are found.
func assertNoSentinel(t *testing.T, label string, data []byte, sentinels ...string) {
	t.Helper()
	s := string(data)
	for _, sentinel := range sentinels {
		if strings.Contains(s, sentinel) {
			t.Errorf("%s contains credential sentinel %q", label, sentinel)
		}
	}
}

// mustJSON parses b as JSON or fails.
func mustJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal %q: %v", string(b), err)
	}
	return v
}

// deepEqualJSON compares two values decoded from JSON for equality.
func deepEqualJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// loadFactorySettings loads Factory settings from a path.
func loadFactorySettings(path string) (*factory.Settings, error) {
	return factory.Load(path)
}

// loadConfigFromPath loads a config from a YAML file path.
func loadConfigFromPath(path string) (*config.Config, error) {
	return config.Load(path)
}

// writeTempFile writes data to a temp file with the given name pattern.
func writeTempFile(t *testing.T, pattern string, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), pattern)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

// getEnvOrEmpty returns the value of an env var or empty string.
func getEnvOrEmpty(key string) string {
	return os.Getenv(key)
}

// lookupKnownAuth looks up a known-auth profile by name.
func lookupKnownAuth(name string) (config.KnownAuth, bool) {
	return config.LookupKnownAuth(name)
}

// jsonMarshalIndent marshals a value to indented JSON.
func jsonMarshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// buildAPIFromConfig builds a handler API from any config, using a single
// shared upstream. Used for compatibility tests that don't need the combined
// installation.
func buildAPIFromConfig(t *testing.T, cfg *config.Config) (*handlers.API, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	client := upstream.NewClient(cfg)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := handlers.NewAPI(cfg, router, client, nil, nil, logger)
	engine := gin.New()
	engine.POST("/v1/chat/completions", api.ChatCompletions)
	engine.POST("/chat/completions", api.ChatCompletions)
	engine.GET("/v1/models", api.Models)
	engine.GET("/models", api.Models)
	return api, engine
}

// entryFor creates a Factory entry for testing.
func entryFor(model, displayName string) factory.Entry {
	return factory.Entry{
		Model:       model,
		DisplayName: displayName,
		Provider:    "generic-chat-completion-api",
		BaseURL:     "http://127.0.0.1:9787",
		APIKey:      "x",
	}
}

// resetAllCaptures clears request captures on all fake upstreams.
func resetAllCaptures(ci *combinedInstallation) {
	for _, fu := range ci.upstreams {
		fu.Capture().Reset()
	}
}

// unused suppresses import errors for time when not all helpers are used.
var _ = time.Second
