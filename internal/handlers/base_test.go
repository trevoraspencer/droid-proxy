package handlers

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/upstream"
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

func TestUpstreamReadFailureMessageDistinguishesCauses(t *testing.T) {
	if got := upstreamReadFailureMessage(upstream.ErrBodyTooLarge, "response"); got != "upstream response body too large" {
		t.Fatalf("too-large message = %q", got)
	}
	if got := upstreamReadFailureMessage(fmt.Errorf("wrapped: %w", upstream.ErrBodyTooLarge), "error"); got != "upstream error body too large" {
		t.Fatalf("wrapped too-large message = %q", got)
	}
	if got := upstreamReadFailureMessage(io.ErrUnexpectedEOF, "response"); got != "failed to read upstream response body" {
		t.Fatalf("read-failure message = %q", got)
	}
}
