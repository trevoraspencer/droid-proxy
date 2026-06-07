// Package server wires the gin engine, middleware, and routes for droid-proxy.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
	"droid-proxy/internal/handlers"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/upstream"
)

// Server holds the gin engine, the http.Server, and the dependencies it serves.
type Server struct {
	cfg     *config.Config
	logger  *logrus.Logger
	engine  *gin.Engine
	oauth   *oauth.Manager
	pool    *oauth.AccountPool
	watcher *oauth.Watcher
}

// New constructs the engine and registers routes. It does not start listening.
func New(cfg *config.Config, logger *logrus.Logger) (*Server, error) {
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	oauthMgr := oauth.NewManager(cfg)

	// Seed the Codex account pool from existing token files.
	// Invalid/unreadable files are logged and skipped; startup continues.
	var seedTokens []*oauth.Token
	if authDir, err := oauthMgr.AuthDir(); err != nil {
		logger.WithError(err).Warn("server: cannot resolve auth dir")
	} else {
		var seedErr error
		seedTokens, seedErr = oauth.LoadCodexTokensFromDir(oauthMgr, authDir, logger)
		if seedErr != nil {
			logger.WithError(seedErr).Warn("server: initial Codex token load failed")
		}
	}
	sel := oauth.NewSelector(cfg.OAuth.LoadBalancing.Strategy)
	var affinity *oauth.AffinityStore
	if cfg.OAuth.LoadBalancing.Strategy == config.LoadBalancingSticky {
		authDir, err := oauthMgr.AuthDir()
		if err != nil {
			logger.WithError(err).Warn("server: cannot resolve auth dir for affinity")
		} else {
			affinityPath, err := oauth.ResolveAffinityPath(cfg, authDir)
			if err != nil {
				logger.WithError(err).Warn("server: cannot resolve affinity path")
			} else {
				affinity, err = oauth.NewAffinityStore(oauth.AffinityOptions{
					Path:       affinityPath,
					TTL:        cfg.OAuth.LoadBalancing.AffinityTTL,
					MaxEntries: cfg.OAuth.LoadBalancing.AffinityMaxEntries,
				})
				if err != nil {
					logger.WithError(err).Warn("server: conversation affinity store unavailable")
					affinity = nil
				}
			}
		}
	}
	pool := oauth.NewAccountPool(seedTokens, time.Now, cfg.OAuth.LoadBalancing, affinity, sel)

	client := upstream.NewClient(cfg)
	api := handlers.NewAPI(cfg, router, client, oauthMgr, pool, logger)

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(RequestID())
	engine.Use(Recovery(logger))
	engine.Use(TraceLog(cfg, logger))
	engine.Use(AccessLog(logger))

	// Health endpoints (no auth)
	engine.GET("/health", handlers.Health)
	engine.GET("/healthz", handlers.Health)
	engine.HEAD("/healthz", handlers.Health)

	// All other routes are auth-gated when client_auth is enabled.
	authed := engine.Group("/", ClientAuth(cfg), RequestBodyLimit(cfg))
	registerAPIRoutes(authed, api)

	return &Server{cfg: cfg, logger: logger, engine: engine, oauth: oauthMgr, pool: pool}, nil
}

// registerAPIRoutes mounts the /v1/* surface plus its prefix-less aliases.
func registerAPIRoutes(rg *gin.RouterGroup, api *handlers.API) {
	mount := func(method, path string, h gin.HandlerFunc) {
		rg.Handle(method, "/v1"+path, h)
		rg.Handle(method, path, h)
	}
	mount(http.MethodGet, "/models", api.Models)
	mount(http.MethodGet, "/oauth/pool-health", api.PoolHealth)
	mount(http.MethodPost, "/chat/completions", api.ChatCompletions)
	mount(http.MethodPost, "/responses", api.Responses)
	mount(http.MethodPost, "/messages", api.Messages)
	mount(http.MethodPost, "/messages/count_tokens", api.CountTokens)
}

// Engine returns the gin engine (used by tests to drive requests via httptest).
func (s *Server) Engine() *gin.Engine { return s.engine }

// Addr is the listen address derived from config.
func (s *Server) Addr() string {
	return net.JoinHostPort(s.cfg.Listen.Host, strconv.Itoa(s.cfg.Listen.Port))
}

// Run starts the HTTP server and blocks until ctx is cancelled or the server errors.
// On ctx cancellation it performs a graceful shutdown with the configured timeout.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		return err
	}
	return s.RunOnListener(ctx, ln)
}

// RunOnListener serves on an already-bound listener. It is primarily useful for
// tests that need the OS to choose a port without releasing it before startup.
func (s *Server) RunOnListener(ctx context.Context, ln net.Listener) error {
	// Start the auth-dir watcher for hot reload of Codex token files.
	watcher, err := oauth.NewWatcher(s.oauth, s.pool, 200*time.Millisecond, s.logger)
	if err != nil {
		s.logger.WithError(err).Warn("server: auth-dir watcher failed to start; hot reload disabled")
	}
	s.watcher = watcher

	srv := s.newHTTPServer()
	errCh := make(chan error, 1)
	go func() {
		s.logger.WithField("addr", ln.Addr().String()).Info("droid-proxy listening")
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		// Stop the watcher before shutting down the HTTP server.
		if s.watcher != nil {
			s.watcher.Close()
		}
		shutdownCtx, cancel := shutdownContext(s.cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.logger.WithError(err).Warn("graceful shutdown failed; forcing close")
			_ = srv.Close()
		}
		<-errCh
		return nil
	case err := <-errCh:
		// Server errored; stop the watcher.
		if s.watcher != nil {
			s.watcher.Close()
		}
		return err
	}
}

func (s *Server) newHTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.Addr(),
		Handler:           s.engine,
		ReadHeaderTimeout: s.cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       s.cfg.Server.ReadTimeout,
		WriteTimeout:      s.cfg.Server.WriteTimeout,
		IdleTimeout:       s.cfg.Server.IdleTimeout,
	}
}

func shutdownContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}
