package oauth

import (
	"net/http"
	"testing"
	"time"
)

func TestParseCodexRateLimitHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "42.5")
	headers.Set("x-codex-primary-window-minutes", "300")
	headers.Set("x-codex-primary-reset-at", "1893456000")
	headers.Set("x-codex-secondary-used-percent", "99")
	quota, resetAt := ParseCodexRateLimitHeaders(headers)
	if quota == nil || quota.Primary == nil || quota.Secondary == nil {
		t.Fatalf("expected quota windows, got quota=%+v reset=%v", quota, resetAt)
	}
	if quota.Primary.UsedPercent != 42.5 || quota.Primary.WindowMinutes == nil || *quota.Primary.WindowMinutes != 300 {
		t.Fatalf("bad primary quota: %+v", quota.Primary)
	}
	if quota.Primary.RemainingPercent == nil || *quota.Primary.RemainingPercent != 57.5 {
		t.Fatalf("bad remaining percent: %+v", quota.Primary)
	}
	if resetAt == nil || resetAt.UTC().Format(time.RFC3339) != "2030-01-01T00:00:00Z" {
		t.Fatalf("bad reset time: %v", resetAt)
	}
}

func TestParseCodexRateLimitHeadersRetryAfter(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "5")
	quota, resetAt := ParseCodexRateLimitHeaders(headers)
	if quota != nil {
		t.Fatalf("unexpected quota: %+v", quota)
	}
	if resetAt == nil || time.Until(*resetAt) <= 0 {
		t.Fatalf("expected future retry reset, got %v", resetAt)
	}
}

func TestParseCodexRateLimitsEvent(t *testing.T) {
	quota := ParseCodexRateLimitsEvent([]byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":60,"reset_at":1893456000},"secondary":{"used_percent":12}}}`))
	if quota == nil || quota.Primary == nil || quota.Secondary == nil {
		t.Fatalf("expected quota from event, got %+v", quota)
	}
	if !quota.Primary.LimitReached {
		t.Fatalf("expected primary to be marked exhausted: %+v", quota.Primary)
	}
	if quota.Secondary.UsedPercent != 12 {
		t.Fatalf("bad secondary quota: %+v", quota.Secondary)
	}
	if got := ParseCodexRateLimitsEvent([]byte(`{"type":"response.output_text.delta","delta":"hi"}`)); got != nil {
		t.Fatalf("unexpected quota from normal event: %+v", got)
	}
}
