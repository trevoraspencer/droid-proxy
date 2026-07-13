package harness

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// Result is all samples for one target × scenario cell.
type Result struct {
	Target   string    `json:"target"`
	Scenario string    `json:"scenario"`
	Baseline bool      `json:"baseline"`
	Skipped  string    `json:"skipped,omitempty"`
	Started  time.Time `json:"started"`
	// WallTime is the wall-clock duration of the measured phase, used for
	// throughput; it excludes warmup.
	WallTime time.Duration `json:"wall_time_ns"`
	Samples  []Sample      `json:"samples"`
}

// Runner executes a benchmark config.
type Runner struct {
	Config Config
	// Log receives progress lines; nil silences progress output.
	Log io.Writer
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

// Run executes every scenario against every target, sequentially per cell so
// targets never contend with each other for local CPU or upstream capacity.
func (r *Runner) Run(ctx context.Context) ([]Result, error) {
	var results []Result
	for _, sc := range r.Config.Scenarios {
		for _, t := range r.Config.Targets {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			if !t.speaks(sc.Protocol) || t.modelFor(sc.Protocol) == "" {
				results = append(results, Result{
					Target: t.Name, Scenario: sc.Name, Baseline: t.Baseline,
					Skipped: "target does not serve protocol " + string(sc.Protocol),
					Started: time.Now(),
				})
				continue
			}
			r.logf("scenario %-28s target %-20s (%d requests, concurrency %d)", sc.Name, t.Name, sc.Requests, sc.Concurrency)
			res := runCell(ctx, t, sc)
			results = append(results, res)
		}
	}
	return results, nil
}

func runCell(ctx context.Context, t Target, sc Scenario) Result {
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	res := Result{Target: t.Name, Scenario: sc.Name, Baseline: t.Baseline, Started: time.Now()}
	model := t.modelFor(sc.Protocol)

	// Warmup establishes connections and, for cache scenarios, primes provider
	// caches. Warmup samples are discarded.
	for i := 0; i < sc.Warmup; i++ {
		body, err := bodyForRequest(sc, model, i)
		if err != nil {
			res.Samples = append(res.Samples, Sample{Err: "build body: " + err.Error()})
			return res
		}
		_ = runOne(ctx, client, t, sc, body)
	}

	type job struct {
		idx  int
		body []byte
	}
	jobs := make(chan job)
	samples := make([]Sample, sc.Requests)
	var wg sync.WaitGroup
	measureStart := time.Now()
	for w := 0; w < sc.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				samples[j.idx] = runOne(ctx, client, t, sc, j.body)
			}
		}()
	}
	for i := 0; i < sc.Requests; i++ {
		body, err := bodyForRequest(sc, model, sc.Warmup+i)
		if err != nil {
			samples[i] = Sample{Err: "build body: " + err.Error()}
			continue
		}
		jobs <- job{idx: i, body: body}
	}
	close(jobs)
	wg.Wait()
	res.WallTime = time.Since(measureStart)
	res.Samples = samples
	return res
}

// bodyForRequest picks the request body for global request index i (warmup
// included) so growing conversations replay deterministically across targets.
func bodyForRequest(sc Scenario, model string, i int) ([]byte, error) {
	turns := sc.HistoryTurns
	if sc.GrowingConversation {
		turns = i
		if sc.HistoryTurns > 0 && turns > sc.HistoryTurns {
			turns = sc.HistoryTurns
		}
	}
	nonce := ""
	if sc.UniquePrompts {
		nonce = fmt.Sprintf("|nonce-%d", i)
	}
	return buildBody(sc, sc.Protocol, model, turns, nonce)
}
