package oauth

import "github.com/trevoraspencer/droid-proxy/internal/config"

// TestPoolLB returns load-balancing settings for unit tests (round-robin, no soft cap).
func TestPoolLB() config.LoadBalancing {
	return config.LoadBalancing{
		Strategy:            config.LoadBalancingRoundRobin,
		QuotaSoftCapPercent: 0,
	}
}
