// Package testutil provides shared test helpers used across internal test
// packages (e.g. handlers and server). Keeping assertion helpers here avoids
// duplication that would otherwise diverge over time.
package testutil

import "testing"

// AssertOAuthHealth checks that a model entry's oauth_auth sub-object contains
// the expected introspection fields and values.
//
// Used by:
//   - internal/handlers/models_test.go
//   - internal/server/server_test.go
func AssertOAuthHealth(t *testing.T, model map[string]any, provider, pinned string, matching, active, disabled, expired int, missing bool) {
	t.Helper()
	health, ok := model["oauth_auth"].(map[string]any)
	if !ok {
		t.Fatalf("missing oauth_auth in %#v", model)
	}
	if health["provider"] != provider || health["pinned_account"] != pinned || health["missing_auth"] != missing {
		t.Fatalf("bad auth health identity: provider=%v pinned=%v missing=%v, want provider=%s pinned=%s missing=%v",
			health["provider"], health["pinned_account"], health["missing_auth"], provider, pinned, missing)
	}
	if int(health["matching_account_count"].(float64)) != matching ||
		int(health["active_count"].(float64)) != active ||
		int(health["disabled_count"].(float64)) != disabled ||
		int(health["expired_or_expiring_count"].(float64)) != expired {
		t.Fatalf("bad auth health counts: matching=%v active=%v disabled=%v expired=%v, want matching=%d active=%d disabled=%d expired=%d",
			health["matching_account_count"], health["active_count"], health["disabled_count"], health["expired_or_expiring_count"],
			matching, active, disabled, expired)
	}
}
