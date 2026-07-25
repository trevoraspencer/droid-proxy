package oauth

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ParseCodexUsageLimitFromBody extracts quota windows from Codex error payloads
// (JSON or SSE-assembled JSON) when response headers are missing or incomplete.
func ParseCodexUsageLimitFromBody(body []byte) *CodexQuota {
	if len(body) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}

	// Direct rate_limits object or nested under error/detail.
	paths := []string{
		"rate_limits",
		"error.rate_limits",
		"detail.rate_limits",
		"usage_limit",
		"error.usage_limit",
	}
	for _, path := range paths {
		if q := parseRateLimitsAt(gjson.GetBytes(body, path)); q != nil {
			return q
		}
	}

	// usage_limit_reached flag with optional nested windows
	if gjson.GetBytes(body, "usage_limit_reached").Bool() ||
		gjson.GetBytes(body, "error.usage_limit_reached").Bool() ||
		gjson.GetBytes(body, "limit_reached").Bool() {
		if q := parseRateLimitsAt(gjson.GetBytes(body, "rate_limits")); q != nil {
			return q
		}
		q := &CodexQuota{}
		if w := parseCodexWindowObject(gjson.GetBytes(body, "primary").Value()); w != nil {
			w.LimitReached = true
			q.Primary = w
		}
		if w := parseCodexWindowObject(gjson.GetBytes(body, "secondary").Value()); w != nil {
			w.LimitReached = true
			q.Secondary = w
		}
		if q.Primary != nil || q.Secondary != nil {
			return q
		}
	}

	// Fallback: generic JSON object map
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	if limits, ok := payload["rate_limits"].(map[string]any); ok {
		return ParseCodexRateLimitsFromMap(limits)
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if limits, ok := errObj["rate_limits"].(map[string]any); ok {
			return ParseCodexRateLimitsFromMap(limits)
		}
	}
	return nil
}

func parseRateLimitsAt(result gjson.Result) *CodexQuota {
	if !result.Exists() {
		return nil
	}
	var limits map[string]any
	if err := json.Unmarshal([]byte(result.Raw), &limits); err != nil {
		return nil
	}
	return ParseCodexRateLimitsFromMap(limits)
}

// ParseCodexRateLimitsFromMap builds a CodexQuota from a rate_limits object.
func ParseCodexRateLimitsFromMap(limits map[string]any) *CodexQuota {
	if limits == nil {
		return nil
	}
	q := &CodexQuota{
		Primary:   parseCodexWindowObject(limits["primary"]),
		Secondary: parseCodexWindowObject(limits["secondary"]),
	}
	if review, ok := limits["code_review"].(map[string]any); ok {
		q.CodeReview = firstNonNilWindow(
			parseCodexWindowObject(review["primary"]),
			parseCodexWindowObject(review["secondary"]),
		)
	}
	if q.Primary == nil && q.Secondary == nil && q.CodeReview == nil {
		return nil
	}
	for _, w := range []*CodexQuotaWindow{q.Primary, q.Secondary, q.CodeReview} {
		if w != nil && w.UsedPercent >= 100 {
			w.LimitReached = true
		}
	}
	return q
}
