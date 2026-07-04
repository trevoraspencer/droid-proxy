package oauth

import "sort"

// MaxAgentUsedPercent returns the highest used_percent across primary and
// secondary Codex quota windows. Unknown or missing quota is treated as 0.
func MaxAgentUsedPercent(q *CodexQuota) float64 {
	if q == nil {
		return 0
	}
	max := 0.0
	for _, w := range []*CodexQuotaWindow{q.Primary, q.Secondary} {
		if w == nil {
			continue
		}
		if w.UsedPercent > max {
			max = w.UsedPercent
		}
	}
	return max
}

// NextAgentResetAt returns the earliest future reset among primary/secondary windows.
func NextAgentResetAt(q *CodexQuota, now int64) *int64 {
	if q == nil {
		return nil
	}
	var best *int64
	for _, w := range []*CodexQuotaWindow{q.Primary, q.Secondary} {
		if w == nil || w.ResetAt == nil || *w.ResetAt <= now {
			continue
		}
		if best == nil || *w.ResetAt < *best {
			v := *w.ResetAt
			best = &v
		}
	}
	return best
}

func sortEligibleByQuota(eligible []*AccountEntry) {
	sort.SliceStable(eligible, func(i, j int) bool {
		pi := MaxAgentUsedPercent(eligible[i].Quota)
		pj := MaxAgentUsedPercent(eligible[j].Quota)
		if pi != pj {
			return pi < pj
		}
		if eligible[i].Selector != eligible[j].Selector {
			return eligible[i].Selector < eligible[j].Selector
		}
		return eligible[i].Path < eligible[j].Path
	})
}

// pickByQuota chooses the eligible account with the lowest max used_percent.
func pickByQuota(eligible []*AccountEntry) *AccountEntry {
	if len(eligible) == 0 {
		return nil
	}
	sortEligibleByQuota(eligible)
	return eligible[0]
}

// filterEligibleBySoftCap removes accounts at or above softCap used_percent.
// If that would empty the set, returns the original eligible slice sorted by quota.
func filterEligibleBySoftCap(eligible []*AccountEntry, softCap float64) []*AccountEntry {
	if softCap <= 0 || len(eligible) == 0 {
		return eligible
	}
	var under []*AccountEntry
	for _, e := range eligible {
		if MaxAgentUsedPercent(e.Quota) < softCap {
			under = append(under, e)
		}
	}
	if len(under) > 0 {
		sortEligibleByQuota(under)
		return under
	}
	out := append([]*AccountEntry(nil), eligible...)
	sortEligibleByQuota(out)
	return out
}
