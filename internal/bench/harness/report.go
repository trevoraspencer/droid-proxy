package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Summary aggregates one Result's samples.
type Summary struct {
	Target   string `json:"target"`
	Scenario string `json:"scenario"`
	Baseline bool   `json:"baseline"`
	Skipped  string `json:"skipped,omitempty"`
	Count    int    `json:"count"`
	Errors   int    `json:"errors"`

	TTFBp50 time.Duration `json:"ttfb_p50_ns"`
	TTFBp95 time.Duration `json:"ttfb_p95_ns"`
	TTFTp50 time.Duration `json:"ttft_p50_ns"`
	TTFTp95 time.Duration `json:"ttft_p95_ns"`

	Totalp50 time.Duration `json:"total_p50_ns"`
	Totalp95 time.Duration `json:"total_p95_ns"`
	Totalp99 time.Duration `json:"total_p99_ns"`

	MeanGap time.Duration `json:"mean_gap_ns"`
	MaxGap  time.Duration `json:"max_gap_ns"`

	Throughput float64 `json:"requests_per_sec"`
	ChunksPerS float64 `json:"chunks_per_sec"`

	PromptTokens int64   `json:"prompt_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	CacheHitPct  float64 `json:"cache_hit_pct"`
}

// Summarize reduces a Result to a Summary.
func Summarize(r Result) Summary {
	s := Summary{Target: r.Target, Scenario: r.Scenario, Baseline: r.Baseline, Skipped: r.Skipped}
	if r.Skipped != "" {
		return s
	}
	var ttfb, ttft, total []time.Duration
	var gapSum, gapMax time.Duration
	var gapN int
	var chunks int
	var streamSeconds float64
	for _, smp := range r.Samples {
		s.Count++
		if !smp.ok() {
			s.Errors++
			continue
		}
		ttfb = append(ttfb, smp.TTFB)
		total = append(total, smp.Total)
		if smp.TTFT > 0 {
			ttft = append(ttft, smp.TTFT)
		}
		if smp.MeanGap > 0 {
			gapSum += smp.MeanGap
			gapN++
		}
		if smp.MaxGap > gapMax {
			gapMax = smp.MaxGap
		}
		chunks += smp.Chunks
		streamSeconds += smp.Total.Seconds()
		s.PromptTokens += smp.Usage.PromptTokens
		s.CachedTokens += smp.Usage.CachedTokens
	}
	s.TTFBp50, s.TTFBp95, _ = percentiles(ttfb)
	s.TTFTp50, s.TTFTp95, _ = percentiles(ttft)
	s.Totalp50, s.Totalp95, s.Totalp99 = percentiles(total)
	if gapN > 0 {
		s.MeanGap = gapSum / time.Duration(gapN)
	}
	s.MaxGap = gapMax
	if r.WallTime > 0 {
		s.Throughput = float64(s.Count-s.Errors) / r.WallTime.Seconds()
	}
	if streamSeconds > 0 {
		s.ChunksPerS = float64(chunks) / streamSeconds
	}
	if s.PromptTokens > 0 {
		s.CacheHitPct = 100 * float64(s.CachedTokens) / float64(s.PromptTokens)
	}
	return s
}

func percentiles(d []time.Duration) (p50, p95, p99 time.Duration) {
	if len(d) == 0 {
		return 0, 0, 0
	}
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(p float64) time.Duration {
		idx := int(p*float64(len(sorted))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return at(0.50), at(0.95), at(0.99)
}

// Report is the serialized output of a run.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Summaries   []Summary `json:"summaries"`
	Results     []Result  `json:"results"`
}

// BuildReport summarizes results into a Report.
func BuildReport(results []Result) Report {
	rep := Report{GeneratedAt: time.Now(), Results: results}
	for _, r := range results {
		rep.Summaries = append(rep.Summaries, Summarize(r))
	}
	return rep
}

// WriteJSON writes the full report (summaries + raw samples) as JSON.
func (rep Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func fmtDur(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

// deltaPct formats the relative slowdown of v against baseline b.
func deltaPct(v, b time.Duration) string {
	if b <= 0 || v <= 0 {
		return ""
	}
	pct := 100 * (float64(v) - float64(b)) / float64(b)
	return fmt.Sprintf("%+.1f%%", pct)
}

// WriteText renders an aligned comparison table grouped by scenario. When a
// baseline target exists, non-baseline rows show relative deltas for TTFT
// (streaming) and total latency.
func (rep Report) WriteText(w io.Writer) {
	byScenario := map[string][]Summary{}
	var order []string
	for _, s := range rep.Summaries {
		if _, ok := byScenario[s.Scenario]; !ok {
			order = append(order, s.Scenario)
		}
		byScenario[s.Scenario] = append(byScenario[s.Scenario], s)
	}
	for _, name := range order {
		group := byScenario[name]
		var base *Summary
		for i := range group {
			if group[i].Baseline && group[i].Skipped == "" {
				base = &group[i]
				break
			}
		}
		fmt.Fprintf(w, "\n=== scenario: %s ===\n", name)
		fmt.Fprintf(w, "%-22s %5s %4s %9s %9s %9s %9s %9s %9s %8s %7s %10s %8s\n",
			"target", "n", "err", "ttfb p50", "ttft p50", "ttft p95", "total p50", "total p95", "gap mean", "max gap", "req/s", "chunks/s", "cache%")
		for _, s := range group {
			if s.Skipped != "" {
				fmt.Fprintf(w, "%-22s SKIPPED: %s\n", s.Target, s.Skipped)
				continue
			}
			cache := "-"
			if s.PromptTokens > 0 {
				cache = fmt.Sprintf("%.1f", s.CacheHitPct)
			}
			fmt.Fprintf(w, "%-22s %5d %4d %9s %9s %9s %9s %9s %9s %8s %7.1f %10.1f %8s\n",
				s.Target, s.Count, s.Errors,
				fmtDur(s.TTFBp50), fmtDur(s.TTFTp50), fmtDur(s.TTFTp95),
				fmtDur(s.Totalp50), fmtDur(s.Totalp95),
				fmtDur(s.MeanGap), fmtDur(s.MaxGap),
				s.Throughput, s.ChunksPerS, cache)
			if base != nil && !s.Baseline {
				var deltas []string
				if d := deltaPct(s.TTFTp50, base.TTFTp50); d != "" {
					deltas = append(deltas, "ttft p50 "+d)
				}
				if d := deltaPct(s.Totalp50, base.Totalp50); d != "" {
					deltas = append(deltas, "total p50 "+d)
				}
				if d := deltaPct(s.TTFBp50, base.TTFBp50); d != "" {
					deltas = append(deltas, "ttfb p50 "+d)
				}
				if len(deltas) > 0 {
					fmt.Fprintf(w, "%-22s   vs %s: %s\n", "", base.Target, strings.Join(deltas, ", "))
				}
			}
		}
	}
	fmt.Fprintln(w)
}

// WriteMarkdown renders the comparison as GitHub-flavored markdown tables.
func (rep Report) WriteMarkdown(w io.Writer) {
	byScenario := map[string][]Summary{}
	var order []string
	for _, s := range rep.Summaries {
		if _, ok := byScenario[s.Scenario]; !ok {
			order = append(order, s.Scenario)
		}
		byScenario[s.Scenario] = append(byScenario[s.Scenario], s)
	}
	fmt.Fprintf(w, "# droid-bench report\n\nGenerated: %s\n", rep.GeneratedAt.Format(time.RFC3339))
	for _, name := range order {
		group := byScenario[name]
		var base *Summary
		for i := range group {
			if group[i].Baseline && group[i].Skipped == "" {
				base = &group[i]
				break
			}
		}
		fmt.Fprintf(w, "\n## %s\n\n", name)
		fmt.Fprintln(w, "| target | n | err | ttfb p50 | ttft p50 | ttft p95 | total p50 | total p95 | gap mean | req/s | chunks/s | cache hit % | vs baseline (total p50) |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|")
		for _, s := range group {
			if s.Skipped != "" {
				fmt.Fprintf(w, "| %s | - | - | - | - | - | - | - | - | - | - | - | skipped: %s |\n", s.Target, s.Skipped)
				continue
			}
			cache := "-"
			if s.PromptTokens > 0 {
				cache = fmt.Sprintf("%.1f", s.CacheHitPct)
			}
			delta := "-"
			if base != nil && !s.Baseline {
				if d := deltaPct(s.Totalp50, base.Totalp50); d != "" {
					delta = d
				}
			} else if s.Baseline {
				delta = "baseline"
			}
			fmt.Fprintf(w, "| %s | %d | %d | %s | %s | %s | %s | %s | %s | %.1f | %.1f | %s | %s |\n",
				s.Target, s.Count, s.Errors,
				fmtDur(s.TTFBp50), fmtDur(s.TTFTp50), fmtDur(s.TTFTp95),
				fmtDur(s.Totalp50), fmtDur(s.Totalp95), fmtDur(s.MeanGap),
				s.Throughput, s.ChunksPerS, cache, delta)
		}
	}
}
