package oauth

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func ParseCodexRateLimitHeaders(headers http.Header) (*CodexQuota, *time.Time) {
	if headers == nil {
		return nil, retryAfterReset(headers)
	}
	quota := &CodexQuota{
		Primary:    parseCodexWindowHeader(headers, "x-codex-primary"),
		Secondary:  parseCodexWindowHeader(headers, "x-codex-secondary"),
		CodeReview: firstCodexWindow(headers, "x-codex-code-review", "x-codex-review", "x-code-review"),
	}
	if quota.Primary == nil && quota.Secondary == nil && quota.CodeReview == nil {
		return nil, retryAfterReset(headers)
	}
	return quota, latestQuotaReset(quota)
}

func ParseCodexRateLimitsEvent(data []byte) *CodexQuota {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil
	}
	if typ, _ := payload["type"].(string); typ != "" && typ != "codex.rate_limits" && payload["rate_limits"] == nil {
		return nil
	}
	quota := &CodexQuota{}
	if limits, ok := payload["rate_limits"].(map[string]any); ok {
		quota.Primary = parseCodexWindowObject(limits["primary"])
		quota.Secondary = parseCodexWindowObject(limits["secondary"])
	}
	if review, ok := payload["code_review_rate_limits"].(map[string]any); ok {
		quota.CodeReview = firstNonNilWindow(parseCodexWindowObject(review["primary"]), parseCodexWindowObject(review["secondary"]))
	}
	if quota.Primary == nil && quota.Secondary == nil && quota.CodeReview == nil {
		return nil
	}
	return quota
}

// RecordCodexUsage persists Codex quota telemetry for a token. When quota
// evidence includes exhausted windows, RateLimitResetAt is set to the
// exhausted-window reset; otherwise the caller-supplied resetAt is stored as
// display telemetry and must not suppress eligibility on its own.
func (m *Manager) RecordCodexUsage(token *Token, quota *CodexQuota, resetAt *time.Time) error {
	if token == nil || token.Provider() != ProviderCodex || strings.TrimSpace(token.path) == "" {
		return nil
	}
	// Serialize the load→merge→save against concurrent usage records and token
	// refreshes for the same file. This shares the per-path refresh mutex so a
	// quota write can never read a stale file and clobber a refresh's new tokens
	// (last-writer-wins), and concurrent requests on the same account don't lose
	// each other's quota updates.
	mu := m.refreshMutex(token.path)
	mu.Lock()
	defer mu.Unlock()
	latest, err := m.loadTokenPath(token.path)
	if err != nil {
		latest = token
	}
	latest.path = token.path
	if quota != nil {
		latest.CodexQuota = mergeCodexQuota(latest.CodexQuota, quota)
		if reset := ExhaustedWindowResetAt(latest.CodexQuota); reset != nil {
			resetAt = reset
		}
	}
	if resetAt != nil {
		latest.RateLimitResetAt = resetAt.UTC().Format(time.RFC3339)
	}
	latest.LastSeenAt = time.Now().UTC().Format(time.RFC3339)
	_, err = m.SaveToken(latest)
	return err
}

func parseCodexWindowHeader(headers http.Header, prefix string) *CodexQuotaWindow {
	pctRaw := strings.TrimSpace(headers.Get(prefix + "-used-percent"))
	if pctRaw == "" {
		return nil
	}
	pct, err := strconv.ParseFloat(pctRaw, 64)
	if err != nil || math.IsNaN(pct) || math.IsInf(pct, 0) {
		return nil
	}
	win := newCodexQuotaWindow(pct)
	if minutes := parseOptionalFloat(headers.Get(prefix + "-window-minutes")); minutes != nil {
		win.WindowMinutes = minutes
	}
	if reset := parseOptionalUnix(headers.Get(prefix + "-reset-at")); reset != nil {
		win.ResetAt = reset
	}
	return win
}

func parseCodexWindowObject(value any) *CodexQuotaWindow {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	pct, ok := numericValue(obj["used_percent"])
	if !ok {
		return nil
	}
	win := newCodexQuotaWindow(pct)
	if minutes, ok := numericValue(obj["window_minutes"]); ok {
		win.WindowMinutes = &minutes
	}
	if reset, ok := numericValue(obj["reset_at"]); ok {
		resetInt := int64(reset)
		win.ResetAt = &resetInt
	}
	return win
}

func newCodexQuotaWindow(used float64) *CodexQuotaWindow {
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	remaining := 100 - used
	return &CodexQuotaWindow{
		UsedPercent:      used,
		RemainingPercent: &remaining,
		LimitReached:     used >= 100,
	}
}

func firstCodexWindow(headers http.Header, prefixes ...string) *CodexQuotaWindow {
	for _, prefix := range prefixes {
		if win := parseCodexWindowHeader(headers, prefix); win != nil {
			return win
		}
	}
	return nil
}

func firstNonNilWindow(values ...*CodexQuotaWindow) *CodexQuotaWindow {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func parseOptionalFloat(raw string) *float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func parseOptionalUnix(raw string) *int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return nil
	}
	return &value
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// RetryAfterTime parses the Retry-After header and returns a future timestamp
// if the value is valid and in the future relative to now. It supports numeric
// seconds and HTTP-date forms. Invalid, zero, negative, or past values return nil.
func RetryAfterTime(headers http.Header, now time.Time) *time.Time {
	if headers == nil {
		return nil
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	// Numeric seconds
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds > 0 {
			tm := now.Add(time.Duration(seconds) * time.Second)
			return &tm
		}
		return nil // zero or negative
	}
	// HTTP-date
	if tm, err := http.ParseTime(raw); err == nil {
		if tm.After(now) {
			return &tm
		}
		return nil // past date
	}
	return nil // unparseable
}

// retryAfterReset parses the Retry-After header and returns a time.Time.
// Unlike RetryAfterTime, which filters for future-only values and accepts an
// explicit "now" parameter for testability, retryAfterReset always computes
// relative to time.Now() for numeric-seconds values and returns past HTTP-date
// values without filtering. Callers that need future-only semantics should use
// RetryAfterTime instead, or check the returned timestamp themselves.
func retryAfterReset(headers http.Header) *time.Time {
	if headers == nil {
		return nil
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		tm := time.Now().Add(time.Duration(seconds) * time.Second)
		return &tm
	}
	if tm, err := http.ParseTime(raw); err == nil {
		return &tm
	}
	return nil
}

// ExhaustedWindowResetAt returns the latest reset time among quota windows
// that have LimitReached=true, or nil if none are exhausted. When multiple
// windows are exhausted, the account remains rate-limited until all of them
// reset, so the latest of those reset times is returned.
func ExhaustedWindowResetAt(quota *CodexQuota) *time.Time {
	if quota == nil {
		return nil
	}
	var latest *time.Time
	for _, win := range []*CodexQuotaWindow{quota.Primary, quota.Secondary, quota.CodeReview} {
		if win == nil || !win.LimitReached || win.ResetAt == nil || *win.ResetAt <= 0 {
			continue
		}
		tm := time.Unix(*win.ResetAt, 0).UTC()
		if latest == nil || tm.After(*latest) {
			latest = &tm
		}
	}
	return latest
}

func latestQuotaReset(quota *CodexQuota) *time.Time {
	if quota == nil {
		return nil
	}
	var latest *time.Time
	for _, win := range []*CodexQuotaWindow{quota.Primary, quota.Secondary, quota.CodeReview} {
		if win == nil || win.ResetAt == nil || *win.ResetAt <= 0 {
			continue
		}
		tm := time.Unix(*win.ResetAt, 0).UTC()
		if latest == nil || tm.After(*latest) {
			latest = &tm
		}
	}
	return latest
}

func mergeCodexQuota(old, next *CodexQuota) *CodexQuota {
	if old == nil {
		return next
	}
	if next == nil {
		return old
	}
	if next.Primary != nil {
		old.Primary = next.Primary
	}
	if next.Secondary != nil {
		old.Secondary = next.Secondary
	}
	if next.CodeReview != nil {
		old.CodeReview = next.CodeReview
	}
	return old
}
