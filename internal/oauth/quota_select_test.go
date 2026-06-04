package oauth

import (
	"testing"
)

func TestPickByQuota_PrefersLowerUsage(t *testing.T) {
	high := &AccountEntry{Path: "/high.json", Selector: "high", Quota: &CodexQuota{Primary: &CodexQuotaWindow{UsedPercent: 90}}}
	low := &AccountEntry{Path: "/low.json", Selector: "low", Quota: &CodexQuota{Primary: &CodexQuotaWindow{UsedPercent: 10}}}
	picked := pickByQuota([]*AccountEntry{high, low})
	if picked.Path != "/low.json" {
		t.Fatalf("picked %s, want /low.json", picked.Path)
	}
}

func TestFilterEligibleBySoftCap_FallbackWhenAllHot(t *testing.T) {
	hot1 := &AccountEntry{Path: "/a.json", Quota: &CodexQuota{Primary: &CodexQuotaWindow{UsedPercent: 95}}}
	hot2 := &AccountEntry{Path: "/b.json", Quota: &CodexQuota{Primary: &CodexQuotaWindow{UsedPercent: 85}}}
	eligible := []*AccountEntry{hot1, hot2}
	filtered := filterEligibleBySoftCap(eligible, 80)
	if len(filtered) != 2 {
		t.Fatalf("expected fallback to all eligible, got %d", len(filtered))
	}
	if filtered[0].Path != "/b.json" {
		t.Fatalf("expected least-used first, got %s", filtered[0].Path)
	}
}