// Command droid-proxy is a localhost HTTP proxy that lets Factory Droid use
// any BYOK / custom model through a single Go binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"droid-proxy/internal/config"
	"droid-proxy/internal/daemon"
	"droid-proxy/internal/logging"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/server"
	"droid-proxy/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "auth":
			runAuth(os.Args[2:])
			return
		case "start":
			runStart(os.Args[2:])
			return
		case "stop":
			runStop()
			return
		case "status":
			runStatus()
			return
		case "service":
			runService(os.Args[2:])
			return
		case "logs":
			runLogs(os.Args[2:])
			return
		}
	}

	runServerCLI(os.Args[1:])
}

func runServerCLI(args []string) {
	fs := flag.NewFlagSet("droid-proxy", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config.yaml")
	envFile := fs.String("env-file", "", "optional env file with API keys (export KEY=...)")
	showVersion := fs.Bool("version", false, "print version and exit")
	foreground := fs.Bool("foreground", false, "run in foreground (used by daemon and launchd)")
	_ = fs.Parse(args)

	if *showVersion {
		fmt.Printf("droid-proxy %s (%s)\n", version.Version, version.Commit)
		return
	}

	if err := runServer(*configPath, *envFile, *foreground); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy error: %v\n", err)
		os.Exit(1)
	}
}

func runServer(configPath, envFile string, foreground bool) error {
	if err := daemon.LoadEnvFile(envFile); err != nil {
		return fmt.Errorf("env file: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if foreground {
		daemon.CleanStalePID()
		if err := daemon.WritePID(); err != nil {
			return err
		}
		defer daemon.RemovePID()
	}

	logger := logging.New(cfg.Logging)
	srv, err := server.New(cfg, logger)
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.WithField("version", version.Version).Info("droid-proxy starting")
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	logger.Info("droid-proxy stopped")
	return nil
}

func runStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	envFile := fs.String("env-file", "", "optional env file with API keys (export KEY=...)")
	foreground := fs.Bool("foreground", false, "run in foreground (internal)")
	_ = fs.Parse(args)

	if *envFile == "" {
		if wd, err := os.Getwd(); err == nil {
			*envFile = daemon.ResolveEnvFile(wd)
		}
	}

	if *foreground {
		if err := runServer(*configPath, *envFile, true); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	daemon.CleanStalePID()
	if pid, running := daemon.IsRunning(); running {
		fmt.Fprintf(os.Stderr, "droid-proxy already running (pid %d)\n", pid)
		os.Exit(1)
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy start error: %v\n", err)
		os.Exit(1)
	}

	child := exec.Command(exe, "start", "--foreground", "--config", *configPath, "--env-file", *envFile)
	child.Env = os.Environ()
	child.Stdout = nil
	child.Stderr = nil
	child.Stdin = nil
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy start error: %v\n", err)
		os.Exit(1)
	}

	// Wait briefly for child to write PID or exit.
	for i := 0; i < 30; i++ {
		if pid, running := daemon.IsRunning(); running {
			fmt.Printf("droid-proxy started (pid %d)\n", pid)
			fmt.Printf("health: curl -s http://127.0.0.1:8787/health\n")
			return
		}
		if err := child.Process.Signal(syscall.Signal(0)); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy exited during startup\n")
			fmt.Fprintf(os.Stderr, "logs: droid-proxy logs\n")
			os.Exit(1)
		}
		//nolint:revive // short startup poll
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "droid-proxy start timed out waiting for PID file\n")
	os.Exit(1)
}

func runStop() {
	if err := daemon.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy stop error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("droid-proxy stopped.")
}

func runStatus() {
	if pid, running := daemon.IsRunning(); running {
		fmt.Printf("droid-proxy is running (pid %d)\n", pid)
		return
	}
	fmt.Println("droid-proxy is not running.")
}

func runService(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: droid-proxy service <install|uninstall> [--config config.yaml]")
		os.Exit(2)
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ExitOnError)
		configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
		_ = fs.Parse(args[1:])
		if err := daemon.InstallLaunchd(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy service install error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("droid-proxy installed as launchd service.")
		fmt.Println(" - Auto-starts on login")
		fmt.Println(" - Auto-restarts on crash")
		fmt.Printf(" - Process name shown to macOS: droid-proxy\n")
		fmt.Printf(" - Logs: %s/stdout.log, %s/stderr.log\n", daemon.StateDir(), daemon.StateDir())
	case "uninstall":
		if err := daemon.UninstallLaunchd(); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy service uninstall error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("droid-proxy removed from launchd services.")
	default:
		fmt.Fprintln(os.Stderr, "usage: droid-proxy service <install|uninstall> [--config config.yaml]")
		os.Exit(2)
	}
}

func runLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	lines := fs.Int("n", 40, "number of log lines to show")
	_ = fs.Parse(args)

	path := filepath.Join(daemon.StateDir(), "stderr.log")
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		path = args[0]
	}

	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy logs error: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: install the service first, or check %s\n", daemon.StateDir())
		os.Exit(1)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy logs error: %v\n", err)
		os.Exit(1)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		fmt.Println("(no log output yet)")
		return
	}
	all := strings.Split(text, "\n")
	start := 0
	if *lines > 0 && len(all) > *lines {
		start = len(all) - *lines
	}
	fmt.Println(strings.Join(all[start:], "\n"))
}

func defaultConfigPath() string {
	for _, path := range []string{"config.local.yaml", "config.yaml"} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "config.yaml"
}

func runAuth(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: droid-proxy auth <codex|xai> --config config.yaml")
		os.Exit(2)
	}
	provider := config.OAuthProvider(strings.ToLower(strings.TrimSpace(args[0])))
	fs := flag.NewFlagSet("auth "+string(provider), flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
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
