package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"droid-proxy/internal/config"
)

// Models lists configured models in OpenAI's /v1/models response shape, plus a
// few proxy-specific fields (display_name, factory_provider, upstream_protocol,
// capabilities, agent_ready) so clients and operators can inspect what each
// alias maps to.
func (a *API) Models(c *gin.Context) {
	models := a.Router.List()
	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		caps := m.ResolvedCapabilities()
		entry := gin.H{
			"id":                 m.Alias,
			"object":             "model",
			"owned_by":           "droid-proxy",
			"created":            time.Now().Unix(),
			"display_name":       displayName(m),
			"factory_provider":   string(m.FactoryProvider),
			"upstream_protocol":  string(m.UpstreamProtocol),
			"upstream_model":     m.UpstreamModel,
			"max_output_tokens":  m.MaxOutputTokens,
			"max_context_tokens": m.MaxContextTokens,
			"capabilities":       caps,
			"agent_ready":        m.AgentReady(),
		}
		if auth := a.oauthAuthHealth(m); auth != nil {
			entry["oauth_auth"] = auth
		}
		data = append(data, entry)
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

func displayName(m *config.Model) string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Alias
}

func (a *API) oauthAuthHealth(m *config.Model) gin.H {
	if m == nil || !m.OAuthProvider.IsValid() {
		return nil
	}
	health := gin.H{
		"provider":                  string(m.OAuthProvider),
		"pinned_account":            m.OAuthAccount,
		"matching_account_count":    0,
		"active_count":              0,
		"disabled_count":            0,
		"expired_or_expiring_count": 0,
		"missing_auth":              true,
	}
	if a == nil || a.OAuth == nil {
		return health
	}
	tokens, err := a.OAuth.LoadTokens(m.OAuthProvider)
	if err != nil {
		return health
	}
	now := time.Now()
	matching := 0
	active := 0
	disabled := 0
	expiredOrExpiring := 0
	for _, token := range tokens {
		if !token.MatchesAccount(m.OAuthAccount) {
			continue
		}
		matching++
		if token.NeedsRefresh(now) {
			expiredOrExpiring++
		}
		if token.Disabled {
			disabled++
			continue
		}
		if !token.NeedsRefresh(now) {
			active++
		}
	}
	health["matching_account_count"] = matching
	health["active_count"] = active
	health["disabled_count"] = disabled
	health["expired_or_expiring_count"] = expiredOrExpiring
	health["missing_auth"] = matching == 0
	return health
}
