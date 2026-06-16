package handlers

import (
	"strings"
	"testing"
)

func TestSafeErrorMessageRedactsDefaultsAndBounds(t *testing.T) {
	redacted := safeErrorMessage(" upstream failed with access_token=plain-oauth-token ")
	if strings.Contains(redacted, "plain-oauth-token") || !strings.Contains(redacted, "access_token=***") {
		t.Fatalf("safeErrorMessage did not redact credential: %q", redacted)
	}

	if got := safeErrorMessage("   "); got != "upstream error" {
		t.Fatalf("blank safeErrorMessage=%q want upstream error", got)
	}

	long := safeErrorMessage(strings.Repeat("x", 5000))
	if len(long) <= 4096 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long safeErrorMessage was not bounded: len=%d suffix=%q", len(long), long[len(long)-3:])
	}
}
