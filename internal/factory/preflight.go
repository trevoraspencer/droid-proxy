package factory

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// PreflightResult reports whether an omitted-port start may proceed.
type PreflightResult struct {
	// Allowed is true when startup may listen on the new default port.
	Allowed bool
	// Reason explains a refusal with sanitized guidance (no secrets or full
	// entries). Empty when Allowed is true.
	Reason string
}

// CoherencePreflight checks whether the selected canonical Factory settings
// file contains an exact high-confidence entry that still targets the old
// loopback origin (the configured host with the pre-migration default port).
//
// This is a read-only preflight: it never writes settings, creates migration
// state, or scans alternate Factory roots. It is intended to be called before
// binding a listener when the config's listen.port is omitted and would
// resolve to the new default.
//
// listenHost is the configured listen.host (defaults to 127.0.0.1 when empty).
// models is the set of configured droid-proxy models (used for alias/provider
// matching). factoryPath is the canonical Factory settings.json path.
func CoherencePreflight(listenHost string, models []*config.Model, factoryPath string) (PreflightResult, error) {
	oldOrigin := oldOriginURL(listenHost)

	// Build the set of (alias, provider) pairs to match against.
	type modelKey struct {
		model    string
		provider string
	}
	known := make(map[modelKey]struct{}, len(models))
	for _, m := range models {
		if m == nil || strings.TrimSpace(m.Alias) == "" {
			continue
		}
		known[modelKey{model: m.Alias, provider: string(m.FactoryProvider)}] = struct{}{}
	}

	// Absent file is treated as "no dependent state".
	data, err := os.ReadFile(factoryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return PreflightResult{Allowed: true}, nil
		}
		return PreflightResult{}, fmt.Errorf("read factory settings: %w", err)
	}

	// Empty or whitespace-only file is treated as "no dependent state".
	if len(strings.TrimSpace(string(data))) == 0 {
		return PreflightResult{Allowed: true}, nil
	}

	// Parse the top-level object.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return PreflightResult{
			Allowed: false,
			Reason: fmt.Sprintf(
				"the selected Factory settings file is malformed and cannot be read; " +
					"make listen.port explicit or run 'droid-proxy migrate-port' to resolve",
			),
		}, nil
	}

	rawModels, ok := root["customModels"]
	if !ok || len(rawModels) == 0 {
		return PreflightResult{Allowed: true}, nil
	}

	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(rawModels, &entries); err != nil {
		return PreflightResult{
			Allowed: false,
			Reason: fmt.Sprintf(
				"the selected Factory settings file has an unreadable customModels list; " +
					"make listen.port explicit or run 'droid-proxy migrate-port' to resolve",
			),
		}, nil
	}

	// Check each entry for an exact high-confidence match against an old origin.
	for _, entry := range entries {
		modelName := jsonString(entry["model"])
		provider := jsonString(entry["provider"])
		baseURL := jsonString(entry["baseUrl"])

		if modelName == "" || provider == "" || baseURL == "" {
			continue
		}
		if _, isKnown := known[modelKey{model: modelName, provider: provider}]; !isKnown {
			continue
		}
		if baseURL == oldOrigin {
			return PreflightResult{
				Allowed: false,
				Reason: fmt.Sprintf(
					"a Factory entry for model %q still targets the old proxy origin %s; "+
						"make listen.port explicit, run 'droid-proxy migrate-port', or resync Factory to update it",
					modelName, oldOrigin,
				),
			}, nil
		}
	}

	return PreflightResult{Allowed: true}, nil
}

// oldOriginURL builds the exact old loopback URL for the given host with the
// pre-migration default port. IPv6 loopback hosts are bracketed per RFC 3986.
func oldOriginURL(host string) string {
	return config.FormatListenURL(host, config.OldDefaultListenPort)
}
