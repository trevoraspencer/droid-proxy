// Command droid-proxy is a localhost HTTP proxy that lets Factory Droid use
// any BYOK / custom model through a single Go binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"droid-proxy/internal/config"
	"droid-proxy/internal/logging"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/server"
	"droid-proxy/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		runAuth(os.Args[2:])
		return
	}

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

func runAuth(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: droid-proxy auth <codex|xai> --config config.yaml")
		os.Exit(2)
	}
	provider := config.OAuthProvider(strings.ToLower(strings.TrimSpace(args[0])))
	fs := flag.NewFlagSet("auth "+string(provider), flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config.yaml")
	noBrowser := fs.Bool("no-browser", false, "print auth URL without opening a browser")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "auth args error: %v\n", err)
		os.Exit(2)
	}
	if !provider.IsValid() {
		fmt.Fprintf(os.Stderr, "unsupported oauth provider %q (must be codex or xai)\n", provider)
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	path, err := oauth.NewManager(cfg).Login(ctx, provider, !*noBrowser)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Saved %s OAuth credentials to %s\n", provider, path)
}
