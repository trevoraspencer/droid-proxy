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
		data = append(data, gin.H{
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
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

func displayName(m *config.Model) string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Alias
}
