package handlers

import (
	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/reasoning"
	"droid-proxy/internal/upstream"
)

// API holds the shared dependencies for every endpoint handler.
type API struct {
	Cfg            *config.Config
	Router         *upstream.Router
	Client         *upstream.Client
	OAuth          *oauth.Manager
	Pool           *oauth.AccountPool
	Logger         *logrus.Logger
	ReasoningCache *reasoning.Cache
}

// NewAPI builds an API from runtime dependencies and a logger.
func NewAPI(cfg *config.Config, router *upstream.Router, client *upstream.Client, oauthMgr *oauth.Manager, pool *oauth.AccountPool, logger *logrus.Logger) *API {
	if oauthMgr == nil {
		oauthMgr = oauth.NewManager(cfg)
	}
	api := &API{Cfg: cfg, Router: router, Client: client, OAuth: oauthMgr, Pool: pool, Logger: logger}
	if cfg.ReasoningCache.Enabled {
		api.ReasoningCache = reasoning.NewCache(cfg.ReasoningCache.MaxEntries, cfg.ReasoningCache.TTL)
	}
	return api
}
