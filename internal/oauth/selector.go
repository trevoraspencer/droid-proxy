package oauth

import (
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// Selector picks a single account from the eligible candidate list.
// Implementations must be safe for concurrent use.
type Selector interface {
	// Select picks one account from eligible. The eligible slice is always
	// sorted deterministically (by selector label, then path). Select must
	// return ErrNoEligibleAccounts when eligible is empty.
	Select(eligible []*AccountEntry) (*AccountEntry, error)
}

// NewSelector creates the appropriate Selector for the given strategy.
// An empty or blank strategy string resolves deterministically to round-robin.
func NewSelector(strategy config.LoadBalancingStrategy, rng ...*rand.Rand) Selector {
	s := config.LoadBalancingStrategy(normalizeStrategy(string(strategy)))
	switch s {
	case config.LoadBalancingFillFirst:
		return &FillFirstSelector{}
	case config.LoadBalancingLeastConnections:
		return &LeastConnectionsSelector{}
	case config.LoadBalancingRandom:
		r := newConcurrencySafeRNG(rng...)
		return &RandomSelector{rng: r}
	default:
		// round-robin is the default for empty/blank/unrecognised
		return &RoundRobinSelector{}
	}
}

// normalizeStrategy returns the canonical strategy string, defaulting to
// round-robin for empty or whitespace-only input.
func normalizeStrategy(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return string(config.LoadBalancingRoundRobin)
	}
	return s
}

// ---------- Round-Robin ----------

// RoundRobinSelector advances a deterministic cursor across eligible accounts.
// Skips to the next eligible account on each call. The cursor does not reset
// on ordinary reloads.
type RoundRobinSelector struct {
	cursor uint64
}

// Select returns the next eligible account in rotation.
func (s *RoundRobinSelector) Select(eligible []*AccountEntry) (*AccountEntry, error) {
	if len(eligible) == 0 {
		return nil, ErrNoEligibleAccounts
	}
	idx := atomic.AddUint64(&s.cursor, 1) - 1
	return eligible[idx%uint64(len(eligible))], nil
}

// ---------- Fill-First ----------

// FillFirstSelector always returns the first eligible account.
type FillFirstSelector struct{}

// Select returns the first eligible account.
func (s *FillFirstSelector) Select(eligible []*AccountEntry) (*AccountEntry, error) {
	if len(eligible) == 0 {
		return nil, ErrNoEligibleAccounts
	}
	return eligible[0], nil
}

// ---------- Least-Connections ----------

// LeastConnectionsSelector selects the eligible account with the lowest
// in-flight count. Ties are broken by deterministic order (sorted position).
type LeastConnectionsSelector struct{}

// Select returns the eligible account with the lowest in-flight count.
// Ties are broken by the deterministic sorted order of the eligible slice.
func (s *LeastConnectionsSelector) Select(eligible []*AccountEntry) (*AccountEntry, error) {
	if len(eligible) == 0 {
		return nil, ErrNoEligibleAccounts
	}
	best := eligible[0]
	bestInFlight := best.InFlight
	for _, e := range eligible[1:] {
		if e.InFlight < bestInFlight {
			best = e
			bestInFlight = e.InFlight
		}
	}
	return best, nil
}

// ---------- Random ----------

// RandomSelector selects uniformly from eligible accounts using a
// concurrency-safe RNG.
type RandomSelector struct {
	mu  sync.Mutex
	rng *rand.Rand
}

// Select returns a random eligible account.
func (s *RandomSelector) Select(eligible []*AccountEntry) (*AccountEntry, error) {
	if len(eligible) == 0 {
		return nil, ErrNoEligibleAccounts
	}
	s.mu.Lock()
	idx := s.rng.Intn(len(eligible))
	s.mu.Unlock()
	return eligible[idx], nil
}

// newConcurrencySafeRNG creates the RNG for the random selector.
// If a Rand is provided via rng, it is used directly (tests can inject
// deterministic sources). Otherwise a new Rand seeded from the default
// source is created.
func newConcurrencySafeRNG(rng ...*rand.Rand) *rand.Rand {
	if len(rng) > 0 && rng[0] != nil {
		return rng[0]
	}
	return rand.New(rand.NewSource(0)) // deterministic seed for reproducibility
}
