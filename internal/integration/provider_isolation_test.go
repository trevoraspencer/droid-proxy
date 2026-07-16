package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// VAL-CROSS-007: Discovery policies remain provider-correct without live
// calls.
//
// Fire Pass uses static curated routers with no request, Baseten uses
// authenticated shared /v1/models, DeepInfra uses unauthenticated
// GET https://api.deepinfra.com/models/list with bare-array model_name
// extraction and exact reported_type: text-generation filtering, and Standard
// Fireworks uses its declared best-effort compatibility request.
// ---------------------------------------------------------------------------

func TestDiscoveryPolicies_FirePassUsesStatic(t *testing.T) {
	// Fire Pass discovery policy is static: the catalog is curated and no
	// remote discovery request is made. Verify the registry configuration.
	ka, ok := lookupKnownAuth("fireworks-fire-pass")
	if !ok {
		t.Fatal("fireworks-fire-pass not in registry")
	}
	if ka.DiscoveryPolicy != "static" {
		t.Errorf("fireworks-fire-pass DiscoveryPolicy = %q, want static", ka.DiscoveryPolicy)
	}
	if len(ka.StaticModels) == 0 {
		t.Error("fireworks-fire-pass has no static catalog entries")
	}
	// The canonical router must be present.
	found := false
	for _, entry := range ka.StaticModels {
		if entry.ID == "accounts/fireworks/routers/glm-5p2-fast" {
			found = true
			break
		}
	}
	if !found {
		t.Error("fireworks-fire-pass static catalog missing canonical router glm-5p2-fast")
	}
}

func TestDiscoveryPolicies_BasetenUsesAuthenticatedRemote(t *testing.T) {
	ka, ok := lookupKnownAuth("baseten")
	if !ok {
		t.Fatal("baseten not in registry")
	}
	// Baseten uses remote (default) discovery, authenticated.
	if ka.DiscoveryPolicy != "" {
		t.Errorf("baseten DiscoveryPolicy = %q, want empty (remote default)", ka.DiscoveryPolicy)
	}
	if ka.DiscoveryNoAuth {
		t.Error("baseten DiscoveryNoAuth = true, want false (authenticated)")
	}
	if ka.ModelsPath != "" {
		t.Errorf("baseten ModelsPath = %q, want empty (defaults to 'models' -> /v1/models)", ka.ModelsPath)
	}
}

func TestDiscoveryPolicies_DeepInfraUsesUnauthenticatedOfficialList(t *testing.T) {
	ka, ok := lookupKnownAuth("deepinfra")
	if !ok {
		t.Fatal("deepinfra not in registry")
	}
	// DeepInfra discovery is separated from inference: unauthenticated,
	// bare-array model_name, exact reported_type filtering.
	if !ka.DiscoveryNoAuth {
		t.Error("deepinfra DiscoveryNoAuth = false, want true")
	}
	if ka.DiscoveryBaseURL != "https://api.deepinfra.com" {
		t.Errorf("deepinfra DiscoveryBaseURL = %q, want https://api.deepinfra.com", ka.DiscoveryBaseURL)
	}
	if ka.ModelsPath != "/models/list" {
		t.Errorf("deepinfra ModelsPath = %q, want /models/list", ka.ModelsPath)
	}
	if ka.DiscoveryIDField != "model_name" {
		t.Errorf("deepinfra DiscoveryIDField = %q, want model_name", ka.DiscoveryIDField)
	}
	if ka.DiscoveryTypeField != "reported_type" {
		t.Errorf("deepinfra DiscoveryTypeField = %q, want reported_type", ka.DiscoveryTypeField)
	}
	if ka.DiscoveryTypeValue != "text-generation" {
		t.Errorf("deepinfra DiscoveryTypeValue = %q, want text-generation", ka.DiscoveryTypeValue)
	}
}

func TestDiscoveryPolicies_StandardFireworksUsesBestEffortRemote(t *testing.T) {
	ka, ok := lookupKnownAuth("fireworks")
	if !ok {
		t.Fatal("fireworks not in registry")
	}
	// Standard Fireworks uses remote (default) best-effort discovery.
	if ka.DiscoveryPolicy != "" {
		t.Errorf("fireworks DiscoveryPolicy = %q, want empty (remote default)", ka.DiscoveryPolicy)
	}
	if ka.DiscoveryNoAuth {
		t.Error("fireworks DiscoveryNoAuth = true, want false (authenticated)")
	}
}

func TestDiscoveryPolicies_NoCrossContamination(t *testing.T) {
	// Verify that each provider's discovery configuration is independent and
	// correct. DeepInfra never falls back to /v1/models or /v1/openai/models.
	diKA, _ := lookupKnownAuth("deepinfra")
	if diKA.ModelsPath == "/v1/models" || diKA.ModelsPath == "/v1/openai/models" {
		t.Error("deepinfra falls back to a forbidden route")
	}
	// Fire Pass never makes a remote request.
	fpKA, _ := lookupKnownAuth("fireworks-fire-pass")
	if fpKA.DiscoveryPolicy != "static" {
		t.Error("fireworks-fire-pass should be static, not remote")
	}
}

// ---------------------------------------------------------------------------
// VAL-CROSS-008: Provider options are isolated per alias and request.
//
// Fireworks and DeepInfra tier semantics and all configured provider options
// appear only on the resolved alias, Baseten options remain Baseten-only,
// configured values preserve JSON type and merge rules, and
// sequential/interleaved requests show no cross-request or cross-provider
// leakage.
// ---------------------------------------------------------------------------

func TestProviderOptions_FireworksTierSemanticsIsolated(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Standard: no service_tier
	// Priority: service_tier: priority
	// Fast: no service_tier (router model)
	tests := []struct {
		alias    string
		hasTier  bool
		tierVal  string
		upstream string
	}{
		{"fw-standard", false, "", "accounts/fireworks/models/glm-4-standard"},
		{"fw-priority", true, "priority", "accounts/fireworks/models/glm-4-priority"},
		{"fw-fast", false, "", "accounts/fireworks/routers/glm-5p2-fast"},
	}

	for _, tc := range tests {
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, tc.alias, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", tc.alias, w.Code)
		}
		cap := ci.upstreams["fireworks"].Capture().Get(0)
		if got := gjson.GetBytes(cap.Body, "model").String(); got != tc.upstream {
			t.Errorf("%s: upstream model = %q, want %q", tc.alias, got, tc.upstream)
		}
		tier := gjson.GetBytes(cap.Body, "service_tier")
		if tc.hasTier {
			if !tier.Exists() || tier.String() != tc.tierVal {
				t.Errorf("%s: service_tier = %q, want %q", tc.alias, tier.Raw, tc.tierVal)
			}
		} else {
			if tier.Exists() {
				t.Errorf("%s: service_tier should be absent, got %q", tc.alias, tier.Raw)
			}
		}
	}
}

func TestProviderOptions_DeepInfraTierPassthrough(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// DeepInfra Standard omits service_tier.
	resetAllCaptures(ci)
	w, _ := sendChat(t, engine, "deepinfra-model", "")
	if w.Code != http.StatusOK {
		t.Fatalf("deepinfra standard: status = %d", w.Code)
	}
	cap := ci.upstreams["deepinfra"].Capture().Get(0)
	if tier := gjson.GetBytes(cap.Body, "service_tier"); tier.Exists() {
		t.Errorf("deepinfra standard should omit service_tier, got %q", tier.Raw)
	}

	// Request-time priority passes through.
	resetAllCaptures(ci)
	w = sendChatRaw(t, engine, `{"model":"deepinfra-model","messages":[{"role":"user","content":"hi"}],"service_tier":"priority"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("deepinfra priority: status = %d", w.Code)
	}
	cap = ci.upstreams["deepinfra"].Capture().Get(0)
	if tier := gjson.GetBytes(cap.Body, "service_tier"); !tier.Exists() || tier.String() != "priority" {
		t.Errorf("deepinfra priority: service_tier = %q, want priority", tier.Raw)
	}

	// Request-time flex passes through.
	resetAllCaptures(ci)
	w = sendChatRaw(t, engine, `{"model":"deepinfra-model","messages":[{"role":"user","content":"hi"}],"service_tier":"flex"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("deepinfra flex: status = %d", w.Code)
	}
	cap = ci.upstreams["deepinfra"].Capture().Get(0)
	if tier := gjson.GetBytes(cap.Body, "service_tier"); !tier.Exists() || tier.String() != "flex" {
		t.Errorf("deepinfra flex: service_tier = %q, want flex", tier.Raw)
	}
}

func TestProviderOptions_ConfiguredExtraArgsIsolatedPerAlias(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// fw-priority has service_tier: priority configured. Send a request to
	// fw-standard (no service_tier configured) and verify it does NOT inherit
	// the priority tier.
	resetAllCaptures(ci)
	w, _ := sendChat(t, engine, "fw-standard", "")
	if w.Code != http.StatusOK {
		t.Fatalf("fw-standard: status = %d", w.Code)
	}
	cap := ci.upstreams["fireworks"].Capture().Get(0)
	if tier := gjson.GetBytes(cap.Body, "service_tier"); tier.Exists() {
		t.Errorf("fw-standard should not inherit service_tier from fw-priority, got %q", tier.Raw)
	}
}

func TestProviderOptions_InterleavedNoLeakage(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Interleave standard and priority requests and verify no tier leakage.
	sequence := []struct {
		alias   string
		hasTier bool
	}{
		{"fw-standard", false},
		{"fw-priority", true},
		{"fw-standard", false},
		{"fw-priority", true},
		{"fw-standard", false},
	}

	resetAllCaptures(ci)
	for _, tc := range sequence {
		w, _ := sendChat(t, engine, tc.alias, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", tc.alias, w.Code)
		}
	}

	caps := ci.upstreams["fireworks"].Capture().All()
	if len(caps) != len(sequence) {
		t.Fatalf("expected %d captures, got %d", len(sequence), len(caps))
	}
	for i, tc := range sequence {
		tier := gjson.GetBytes(caps[i].Body, "service_tier")
		if tc.hasTier {
			if !tier.Exists() || tier.String() != "priority" {
				t.Errorf("request %d (%s): service_tier = %q, want priority", i, tc.alias, tier.Raw)
			}
		} else {
			if tier.Exists() {
				t.Errorf("request %d (%s): service_tier leaked = %q", i, tc.alias, tier.Raw)
			}
		}
	}
}

func TestProviderOptions_BasetenOptionsRemainBasetenOnly(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Send a request to baseten-model with an option. Verify it reaches the
	// baseten upstream but not other providers' upstreams.
	resetAllCaptures(ci)
	w := sendChatRaw(t, engine, `{"model":"baseten-model","messages":[{"role":"user","content":"hi"}],"temperature":0.7,"max_tokens":100}`)
	if w.Code != http.StatusOK {
		t.Fatalf("baseten: status = %d", w.Code)
	}
	// Only baseten upstream should have received a request.
	if ci.upstreams["fireworks"].Capture().Count() != 0 {
		t.Errorf("fireworks upstream received baseten request")
	}
	if ci.upstreams["deepinfra"].Capture().Count() != 0 {
		t.Errorf("deepinfra upstream received baseten request")
	}
	cap := ci.upstreams["baseten"].Capture().Get(0)
	if got := gjson.GetBytes(cap.Body, "temperature").Float(); got != 0.7 {
		t.Errorf("baseten temperature = %v, want 0.7", got)
	}
	if got := gjson.GetBytes(cap.Body, "max_tokens").Int(); got != 100 {
		t.Errorf("baseten max_tokens = %d, want 100", got)
	}
}

// ---------------------------------------------------------------------------
// VAL-CROSS-012: Provider failures do not corrupt shared state.
//
// Credential, discovery, alias, config, upstream error, truncated stream, and
// canceled stream failures affect only the selected provider/request.
// Every unaffected alias still succeeds afterward.
// ---------------------------------------------------------------------------

func TestProviderFailures_UpstreamErrorDoesNotCorruptState(t *testing.T) {
	ci := newCombinedInstallation(t)

	// Set an error on the Fireworks fake.
	ci.upstreams["fireworks"].SetResponse(`{"error":{"message":"internal error"}}`, http.StatusInternalServerError)

	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Send a failing request to Fireworks. Upstream HTTP errors are relayed
	// with the exact upstream status code.
	resetAllCaptures(ci)
	w, _ := sendChat(t, engine, "fw-standard", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("fireworks error: status = %d, want 500", w.Code)
	}

	// Verify Baseten and DeepInfra still succeed.
	for _, alias := range []string{"baseten-model", "deepinfra-model"} {
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, alias, "")
		if w.Code != http.StatusOK {
			t.Errorf("%s failed after fireworks error: status = %d", alias, w.Code)
		}
	}

	// Verify config and Factory files are unchanged.
	cfgHash := fileHash(t, ci.configPath)
	factoryHash := fileHash(t, ci.factoryPath)
	_ = ci.loadFromDisk()
	if fileHash(t, ci.configPath) != cfgHash {
		t.Error("config changed after provider failure")
	}
	if fileHash(t, ci.factoryPath) != factoryHash {
		t.Error("factory settings changed after provider failure")
	}
}

func TestProviderFailures_UnknownAliasDoesNotAffectOthers(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Send a request with an unknown alias (local failure).
	resetAllCaptures(ci)
	w := sendChatRaw(t, engine, `{"model":"nonexistent","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
		t.Fatalf("unknown alias: status = %d", w.Code)
	}

	// Verify all known aliases still succeed.
	for _, alias := range ci.factoryAliases() {
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, alias, "")
		if w.Code != http.StatusOK {
			t.Errorf("%s failed after unknown alias: status = %d", alias, w.Code)
		}
	}
}

func TestProviderFailures_TruncatedStreamRecovers(t *testing.T) {
	ci := newCombinedInstallation(t)

	// Set a truncated SSE response on the DeepInfra fake.
	ci.upstreams["deepinfra"].SetResponse("data: {\"id\":\"di-trunc\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", http.StatusOK)

	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Send a streaming request that will be truncated.
	resetAllCaptures(ci)
	w := sendChatRaw(t, engine, `{"model":"deepinfra-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("truncated stream status = %d", w.Code)
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "stream_truncated") {
		t.Errorf("truncated stream should emit stream_truncated event, got: %s", bodyStr)
	}

	// Set a healthy response and verify a subsequent request succeeds.
	ci.upstreams["deepinfra"].SetResponse(`{"id":"di-ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`, http.StatusOK)
	resetAllCaptures(ci)
	w = sendChatRaw(t, engine, `{"model":"deepinfra-model","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("post-truncation request: status = %d", w.Code)
	}
}

func TestProviderFailures_PostFaultSuccessMatrix(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Inject failures on each provider one at a time, then verify all others
	// still succeed.
	faults := []struct {
		upstream string
		alias    string
	}{
		{"fireworks", "fw-standard"},
		{"baseten", "baseten-model"},
		{"deepinfra", "deepinfra-model"},
	}

	for _, fault := range faults {
		// Set an error on one provider.
		ci.upstreams[fault.upstream].SetResponse(`{"error":{"message":"fail"}}`, http.StatusInternalServerError)

		// Verify the faulted provider fails.
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, fault.alias, "")
		if w.Code == http.StatusOK {
			t.Errorf("%s should have failed but got 200", fault.alias)
		}

		// Restore healthy response.
		ci.upstreams[fault.upstream].SetResponse(
			fmt.Sprintf(`{"id":"%s-ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`, fault.upstream),
			http.StatusOK)

		// Verify all providers succeed again.
		for _, alias := range ci.factoryAliases() {
			resetAllCaptures(ci)
			w, _ := sendChat(t, engine, alias, "")
			if w.Code != http.StatusOK {
				t.Errorf("post-fault: %s failed after %s fault: status = %d", alias, fault.alias, w.Code)
			}
		}
	}
}
