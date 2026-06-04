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

func TestRetryAfterTime(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		header string
		want   time.Time
		nil    bool
	}{
		{
			name:   "numeric seconds",
			header: "120",
			want:   now.Add(120 * time.Second),
		},
		{
			name:   "zero seconds returns nil",
			header: "0",
			nil:    true,
		},
		{
			name:   "negative seconds returns nil",
			header: "-5",
			nil:    true,
		},
		{
			name:   "HTTP-date future (RFC1123/GMT)",
			header: "Thu, 15 Jan 2026 12:05:00 GMT",
			want:   time.Date(2026, 1, 15, 12, 5, 0, 0, time.UTC),
		},
		{
			name:   "HTTP-date past returns nil",
			header: "Wed, 14 Jan 2026 00:00:00 GMT",
			nil:    true,
		},
		{
			name:   "invalid returns nil",
			header: "not-a-date",
			nil:    true,
		},
		{
			name: "empty returns nil",
			nil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.header != "" {
				h.Set("Retry-After", tt.header)
			}
			got := RetryAfterTime(h, now)
			if tt.nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil result for header %q", tt.header)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}

	// nil headers
	if got := RetryAfterTime(nil, now); got != nil {
		t.Fatalf("expected nil for nil headers, got %v", got)
	}
}

func TestLatestQuotaReset(t *testing.T) {
	// No quota windows
	if got := LatestQuotaReset(nil); got != nil {
		t.Fatalf("expected nil for nil quota, got %v", got)
	}

	// Empty quota
	if got := LatestQuotaReset(&CodexQuota{}); got != nil {
		t.Fatalf("expected nil for empty quota, got %v", got)
	}

	// Single window with reset
	resetUnix := int64(1893456000) // 2030-01-01T00:00:00Z
	expected := time.Unix(resetUnix, 0).UTC()
	quota := &CodexQuota{
		Primary: &CodexQuotaWindow{UsedPercent: 50, ResetAt: &resetUnix},
	}
	got := LatestQuotaReset(quota)
	if got == nil || !got.Equal(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}

	// Multiple windows: latest wins
	earlier := int64(1893456000) // 2030-01-01
	later := int64(1924992000)   // 2031-01-01
	quota2 := &CodexQuota{
		Primary:   &CodexQuotaWindow{UsedPercent: 50, ResetAt: &earlier},
		Secondary: &CodexQuotaWindow{UsedPercent: 80, ResetAt: &later},
	}
	expectedLater := time.Unix(later, 0).UTC()
	got2 := LatestQuotaReset(quota2)
	if got2 == nil || !got2.Equal(expectedLater) {
		t.Fatalf("expected latest reset %v, got %v", expectedLater, got2)
	}

	// Zero or negative reset_at is ignored
	zero := int64(0)
	neg := int64(-1)
	quota3 := &CodexQuota{
		Primary:   &CodexQuotaWindow{UsedPercent: 50, ResetAt: &zero},
		Secondary: &CodexQuotaWindow{UsedPercent: 80, ResetAt: &neg},
	}
	if got3 := LatestQuotaReset(quota3); got3 != nil {
		t.Fatalf("expected nil for zero/negative reset_at, got %v", got3)
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
