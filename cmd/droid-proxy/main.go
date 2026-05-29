// Command droid-proxy is a localhost HTTP proxy that lets Factory Droid use
// any BYOK / custom model through a single Go binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"droid-proxy/internal/config"
	"droid-proxy/internal/logging"
	"droid-proxy/internal/server"
	"droid-proxy/internal/version"
)

func main() {
	var (
		configPath  = flag.String("config", "config.yaml", "path to config.yaml")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Printf("droid-proxy %s (%s)\n", version.Version, version.Commit)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(2)
	}

	logger := logging.New(cfg.Logging)
	srv, err := server.New(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("init server")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.WithField("version", version.Version).Info("droid-proxy starting")
	if err := srv.Run(ctx); err != nil {
		logger.WithError(err).Fatal("server exited with error")
	}
	logger.Info("droid-proxy stopped")
}
