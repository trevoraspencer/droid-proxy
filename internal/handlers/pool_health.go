package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// PoolHealth returns a read-only snapshot of the Codex account pool.
// It never refreshes tokens, calls upstreams, selects accounts, updates
// in-flight counters, or changes cooldown/health state.
// The response shape is deterministic and secret-safe: no access tokens,
// refresh tokens, ID tokens, raw token JSON, or full token file paths are
// exposed.
func (a *API) PoolHealth(c *gin.Context) {
	if a.Pool == nil {
		c.JSON(http.StatusOK, gin.H{
			"object":   "oauth_pool_health",
			"provider": "codex",
			"accounts": []any{},
		})
		return
	}

	snap := a.Pool.Snapshot()
	accounts := make([]any, 0, len(snap.Accounts))
	for i := range snap.Accounts {
		accounts = append(accounts, snap.Accounts[i])
	}

	out := gin.H{
		"object":              "oauth_pool_health",
		"provider":            "codex",
		"strategy":            snap.Strategy,
		"codex_account_count": snap.CodexAccounts,
		"eligible_count":      snap.EligibleCount,
		"accounts":            accounts,
	}
	if snap.Affinity != nil {
		out["affinity"] = snap.Affinity
	}
	c.JSON(http.StatusOK, out)
}
