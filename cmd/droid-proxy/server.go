package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	"github.com/trevoraspencer/droid-proxy/internal/logging"
	"github.com/trevoraspencer/droid-proxy/internal/server"
	"github.com/trevoraspencer/droid-proxy/internal/version"
)

func runServerCLI(args []string) {
	fs := flag.NewFlagSet("droid-proxy", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	envFile := fs.String("env-file", "", "optional env file with API keys (export KEY=...)")
	showVersion := fs.Bool("version", false, "print version and exit")
	foreground := fs.Bool("foreground", false, "run in foreground (used by daemon and launchd)")
	_ = fs.Parse(args)

	if *showVersion {
		fmt.Printf("%s\n", version.String())
		return
	}

	if err := runServer(*configPath, *envFile, *foreground); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy error: %v\n", err)
		os.Exit(1)
	}
}

func runServer(configPath, envFile string, foreground bool) error {
	workDir := configWorkDir(configPath)
	if envFile == "" {
		envFile = defaultEnvFileForConfig(configPath)
	}
	if err := daemon.LoadLayeredEnv(workDir, envFile); err != nil {
		return fmt.Errorf("env file: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// Every process start that would apply the new 9787 default to a config
	// with no explicit listen.port performs a read-only coherence preflight
	// before binding. This applies to direct foreground and background starts,
	// launchd/systemd login-time starts, crash recovery, setup/service starts,
	// and verified or unverified restarts. The automatic-migration opt-out
	// does not disable this preflight.
	if err := omittedPortPreflight(cfg); err != nil {
		return err
	}

	if foreground {
		daemon.CleanStalePID()
		if err := daemon.WritePID(); err != nil {
			return err
		}
		defer daemon.RemovePID()
		meta, err := runtimeMetadata(configPath, envFile, workDir)
		if err != nil {
			daemon.RemovePID()
			return err
		}
		if err := daemon.WriteRuntimeMetadata(meta); err != nil {
			daemon.RemovePID()
			return fmt.Errorf("runtime metadata: %w", err)
		}
		defer daemon.RemoveRuntimeMetadata()
	}

	logger := logging.New(cfg.Logging)
	srv, err := server.New(cfg, logger)
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.WithField("version", version.Current().Version).Info("droid-proxy starting")
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	logger.Info("droid-proxy stopped")
	return nil
}
