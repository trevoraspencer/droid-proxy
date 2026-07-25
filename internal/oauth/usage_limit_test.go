package oauth

import "testing"

func TestParseCodexUsageLimitFromBody(t *testing.T) {
	body := []byte(`{"usage_limit_reached":true,"rate_limits":{"primary":{"used_percent":100,"reset_at":1893456000}}}`)
	q := ParseCodexUsageLimitFromBody(body)
	if q == nil || q.Primary == nil || !q.Primary.LimitReached {
		t.Fatalf("expected exhausted primary quota, got %+v", q)
	}
}
