package handlers

import (
	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
	"droid-proxy/internal/reasoning"
	"droid-proxy/internal/upstream"
)

// API holds the shared dependencies for every endpoint handler.
type API struct {
	Cfg            *config.Config
	Router         *upstream.Router
	Client         *upstream.Client
	Logger         *logrus.Logger
	ReasoningCache *reasoning.Cache
}

// NewAPI builds an API from Deps and a logger.
func NewAPI(d Deps, logger *logrus.Logger) *API {
	api := &API{Cfg: d.Cfg, Router: d.Router, Client: d.Client, Logger: logger}
	if d.Cfg.ReasoningCache.Enabled {
		api.ReasoningCache = reasoning.NewCache(d.Cfg.ReasoningCache.MaxEntries, d.Cfg.ReasoningCache.TTL)
	}
	return api
}
