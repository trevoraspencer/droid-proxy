package harness

import (
	"fmt"
	"io"
	"math"
	"time"
)

// MeanSD is a mean with sample standard deviation.
type MeanSD struct {
	Mean float64 `json:"mean"`
	SD   float64 `json:"sd"`
	N    int     `json:"n"`
}

func meanSD(values []float64) MeanSD {
	n := len(values)
	if n == 0 {
		return MeanSD{}
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(n)
	if n < 2 {
		return MeanSD{Mean: mean, N: n}
	}
	var ss float64
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	return MeanSD{Mean: mean, SD: math.Sqrt(ss / float64(n-1)), N: n}
}

// RepAggregate summarizes one target × scenario cell across interleaved
// repetitions. Latency fields aggregate the per-rep p50s (milliseconds).
// Delta fields are paired: each rep's p50 is compared with the baseline
// target's p50 from the same rep, which cancels host-load drift, then the
// per-rep delta percentages are averaged.
type RepAggregate struct {
	Scenario string `json:"scenario"`
	Target   string `json:"target"`
	Baseline bool   `json:"baseline"`
	Reps     int    `json:"reps"`
	Errors   int    `json:"errors"`

	TTFBp50ms  MeanSD `json:"ttfb_p50_ms"`
	TTFTp50ms  MeanSD `json:"ttft_p50_ms"`
	Totalp50ms MeanSD `json:"total_p50_ms"`

	// Paired deltas vs the baseline target, in percent. Absent (N==0) for the
	// baseline itself or when no baseline ran in a rep.
	TTFBDeltaPct  MeanSD `json:"ttfb_delta_pct"`
	TTFTDeltaPct  MeanSD `json:"ttft_delta_pct"`
	TotalDeltaPct MeanSD `json:"total_delta_pct"`
}

// Aggregate groups per-rep summaries into per-cell aggregates with paired
// deltas. Order follows first appearance in summaries.
func Aggregate(summaries []Summary) []RepAggregate {
	type cellKey struct{ scenario, target string }
	cells := map[cellKey][]Summary{}
	var order []cellKey
	baselines := map[string]map[int]Summary{} // scenario → rep → baseline summary
	for _, s := range summaries {
		if s.Skipped != "" {
			continue
		}
		k := cellKey{s.Scenario, s.Target}
		if _, ok := cells[k]; !ok {
			order = append(order, k)
		}
		cells[k] = append(cells[k], s)
		if s.Baseline {
			if baselines[s.Scenario] == nil {
				baselines[s.Scenario] = map[int]Summary{}
			}
			baselines[s.Scenario][s.Rep] = s
		}
	}

	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	var out []RepAggregate
	for _, k := range order {
		group := cells[k]
		agg := RepAggregate{Scenario: k.scenario, Target: k.target, Baseline: group[0].Baseline, Reps: len(group)}
		var ttfb, ttft, total []float64
		var dTTFB, dTTFT, dTotal []float64
		for _, s := range group {
			agg.Errors += s.Errors
			ttfb = append(ttfb, ms(s.TTFBp50))
			total = append(total, ms(s.Totalp50))
			if s.TTFTp50 > 0 {
				ttft = append(ttft, ms(s.TTFTp50))
			}
			if s.Baseline {
				continue
			}
			base, ok := baselines[s.Scenario][s.Rep]
			if !ok {
				continue
			}
			if pct, ok := pairedDelta(s.TTFBp50, base.TTFBp50); ok {
				dTTFB = append(dTTFB, pct)
			}
			if pct, ok := pairedDelta(s.TTFTp50, base.TTFTp50); ok {
				dTTFT = append(dTTFT, pct)
			}
			if pct, ok := pairedDelta(s.Totalp50, base.Totalp50); ok {
				dTotal = append(dTotal, pct)
			}
		}
		agg.TTFBp50ms = meanSD(ttfb)
		agg.TTFTp50ms = meanSD(ttft)
		agg.Totalp50ms = meanSD(total)
		agg.TTFBDeltaPct = meanSD(dTTFB)
		agg.TTFTDeltaPct = meanSD(dTTFT)
		agg.TotalDeltaPct = meanSD(dTotal)
		out = append(out, agg)
	}
	return out
}

func pairedDelta(v, base time.Duration) (float64, bool) {
	if v <= 0 || base <= 0 {
		return 0, false
	}
	return 100 * (float64(v) - float64(base)) / float64(base), true
}

func fmtMeanSD(m MeanSD, unit string) string {
	if m.N == 0 {
		return "-"
	}
	if m.N < 2 {
		return fmt.Sprintf("%.2f%s", m.Mean, unit)
	}
	return fmt.Sprintf("%.2f±%.2f%s", m.Mean, m.SD, unit)
}

// writeAggregates renders the cross-rep table grouped by scenario.
func writeAggregates(w io.Writer, aggs []RepAggregate) {
	var scenario string
	for _, a := range aggs {
		if a.Scenario != scenario {
			scenario = a.Scenario
			fmt.Fprintf(w, "\n=== scenario: %s (per-rep p50, mean±sd over %d interleaved reps; deltas paired per rep) ===\n", scenario, a.Reps)
			fmt.Fprintf(w, "%-22s %4s %16s %16s %16s %14s %14s %14s\n",
				"target", "err", "ttfb p50 ms", "ttft p50 ms", "total p50 ms", "Δttfb %", "Δttft %", "Δtotal %")
		}
		mark := ""
		if a.Baseline {
			mark = " (baseline)"
		}
		fmt.Fprintf(w, "%-22s %4d %16s %16s %16s %14s %14s %14s\n",
			a.Target+mark, a.Errors,
			fmtMeanSD(a.TTFBp50ms, ""), fmtMeanSD(a.TTFTp50ms, ""), fmtMeanSD(a.Totalp50ms, ""),
			fmtMeanSD(a.TTFBDeltaPct, ""), fmtMeanSD(a.TTFTDeltaPct, ""), fmtMeanSD(a.TotalDeltaPct, ""))
	}
	fmt.Fprintln(w)
}

// writeAggregatesMarkdown renders the cross-rep aggregates as markdown tables.
func writeAggregatesMarkdown(w io.Writer, aggs []RepAggregate) {
	var scenario string
	for _, a := range aggs {
		if a.Scenario != scenario {
			scenario = a.Scenario
			fmt.Fprintf(w, "\n## %s (mean±sd over %d interleaved reps)\n\n", scenario, a.Reps)
			fmt.Fprintln(w, "| target | err | ttfb p50 (ms) | ttft p50 (ms) | total p50 (ms) | Δttfb % | Δttft % | Δtotal % |")
			fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|")
		}
		name := a.Target
		if a.Baseline {
			name += " *(baseline)*"
		}
		fmt.Fprintf(w, "| %s | %d | %s | %s | %s | %s | %s | %s |\n",
			name, a.Errors,
			fmtMeanSD(a.TTFBp50ms, ""), fmtMeanSD(a.TTFTp50ms, ""), fmtMeanSD(a.Totalp50ms, ""),
			fmtMeanSD(a.TTFBDeltaPct, ""), fmtMeanSD(a.TTFTDeltaPct, ""), fmtMeanSD(a.TotalDeltaPct, ""))
	}
}
