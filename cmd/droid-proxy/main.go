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
	"droid-proxy/internal/tui"
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
		case "config", "onboard":
			runConfig(os.Args[2:])
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
	wd, _ := os.Getwd()
	if err := daemon.LoadLayeredEnv(wd, envFile); err != nil {
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

func runConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	_ = fs.Parse(args)
	loadConfigEnv()
	if err := tui.Run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy config error: %v\n", err)
		os.Exit(1)
	}
}

func runAuth(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: droid-proxy auth <codex|xai> [--device] --config config.yaml")
		fmt.Fprintln(os.Stderr, "       droid-proxy auth status [codex|xai] --config config.yaml")
		fmt.Fprintln(os.Stderr, "       droid-proxy auth <enable|disable|logout> <provider> <account> --config config.yaml")
		os.Exit(2)
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "status":
		runAuthStatus(args[1:])
		return
	case "disable":
		runAuthToggle(args[1:], true)
		return
	case "enable":
		runAuthToggle(args[1:], false)
		return
	case "logout":
		runAuthLogout(args[1:])
		return
	}
	provider := config.OAuthProvider(strings.ToLower(strings.TrimSpace(args[0])))
	fs := flag.NewFlagSet("auth "+string(provider), flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	noBrowser := fs.Bool("no-browser", false, "print auth URL without opening a browser")
	device := fs.Bool("device", false, "use Codex device-code login instead of the local callback")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "auth args error: %v\n", err)
		os.Exit(2)
	}
	if !provider.IsValid() {
		fmt.Fprintf(os.Stderr, "unsupported oauth provider %q (must be codex or xai)\n", provider)
		os.Exit(2)
	}
	loadConfigEnv()
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	manager := oauth.NewManager(cfg)
	var path string
	if *device {
		path, err = manager.LoginDevice(ctx, provider, !*noBrowser)
	} else {
		path, err = manager.Login(ctx, provider, !*noBrowser)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Saved %s OAuth credentials to %s\n", provider, path)
}

func runAuthStatus(args []string) {
	providers, flagArgs := authStatusProviders(args)
	fs := flag.NewFlagSet("auth status", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintf(os.Stderr, "auth status args error: %v\n", err)
		os.Exit(2)
	}
	manager, err := authManagerFromConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth status error: %v\n", err)
		os.Exit(2)
	}
	out, err := formatAuthStatus(manager, providers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth status error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(out)
}

func authStatusProviders(args []string) ([]config.OAuthProvider, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		provider := config.OAuthProvider(strings.ToLower(strings.TrimSpace(args[0])))
		if !provider.IsValid() {
			fmt.Fprintf(os.Stderr, "unsupported oauth provider %q (must be codex or xai)\n", provider)
			os.Exit(2)
		}
		return []config.OAuthProvider{provider}, args[1:]
	}
	return []config.OAuthProvider{config.OAuthProviderCodex, config.OAuthProviderXAI}, args
}

func runAuthToggle(args []string, disabled bool) {
	provider, account, flagArgs := parseAuthAccountArgs("auth enable|disable", args)
	fs := flag.NewFlagSet("auth toggle", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintf(os.Stderr, "auth args error: %v\n", err)
		os.Exit(2)
	}
	manager, err := authManagerFromConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth error: %v\n", err)
		os.Exit(2)
	}
	token, err := manager.SetTokenDisabled(provider, account, disabled)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth error: %v\n", err)
		os.Exit(1)
	}
	action := "Enabled"
	if disabled {
		action = "Disabled"
	}
	fmt.Printf("%s %s OAuth account %s (%s)\n", action, provider, token.AccountSelector(), token.Path())
}

func runAuthLogout(args []string) {
	provider, account, flagArgs := parseAuthAccountArgs("auth logout", args)
	fs := flag.NewFlagSet("auth logout", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintf(os.Stderr, "auth logout args error: %v\n", err)
		os.Exit(2)
	}
	manager, err := authManagerFromConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth logout error: %v\n", err)
		os.Exit(2)
	}
	path, err := manager.DeleteToken(provider, account)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth logout error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Deleted %s OAuth credentials for %s from %s\n", provider, account, path)
}

func parseAuthAccountArgs(usage string, args []string) (config.OAuthProvider, string, []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: droid-proxy %s <provider> <account> --config config.yaml\n", usage)
		os.Exit(2)
	}
	provider := config.OAuthProvider(strings.ToLower(strings.TrimSpace(args[0])))
	if !provider.IsValid() {
		fmt.Fprintf(os.Stderr, "unsupported oauth provider %q (must be codex or xai)\n", provider)
		os.Exit(2)
	}
	account := strings.TrimSpace(args[1])
	if account == "" {
		fmt.Fprintln(os.Stderr, "auth account selector is required")
		os.Exit(2)
	}
	return provider, account, args[2:]
}

func authManagerFromConfig(configPath string) (*oauth.Manager, error) {
	loadConfigEnv()
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return oauth.NewManager(cfg), nil
}

// loadConfigEnv loads API keys from the managed secrets file and any repo
// env file so config.Load validation passes for commands that don't run the
// server.
func loadConfigEnv() {
	wd, _ := os.Getwd()
	_ = daemon.LoadLayeredEnv(wd, "")
}

func formatAuthStatus(manager *oauth.Manager, providers []config.OAuthProvider) (string, error) {
	if manager == nil {
		return "", fmt.Errorf("oauth manager is nil")
	}
	authDir, err := manager.AuthDir()
	if err != nil {
		return "", fmt.Errorf("resolve auth dir: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "OAuth auth directory: %s\n", authDir)
	for _, provider := range providers {
		tokens, err := manager.LoadTokens(provider)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s:\n", provider)
		if len(tokens) == 0 {
			b.WriteString("  (no accounts)\n")
			continue
		}
		for _, token := range tokens {
			account := token.AccountSelector()
			if account == "" {
				account = "(default)"
			}
			fmt.Fprintf(&b, "  - provider: %s\n", provider)
			fmt.Fprintf(&b, "    account: %s\n", account)
			if token.Email != "" {
				fmt.Fprintf(&b, "    email: %s\n", token.Email)
			}
			if token.Subject != "" {
				fmt.Fprintf(&b, "    sub: %s\n", token.Subject)
			}
			if token.AccountID != "" {
				fmt.Fprintf(&b, "    account_id: %s\n", token.AccountID)
			}
			if token.Expired != "" {
				fmt.Fprintf(&b, "    expires: %s\n", token.Expired)
			}
			if token.LastRefresh != "" {
				fmt.Fprintf(&b, "    last_refresh: %s\n", token.LastRefresh)
			}
			fmt.Fprintf(&b, "    disabled: %t\n", token.Disabled)
			fmt.Fprintf(&b, "    path: %s\n", token.Path())
		}
	}
	return b.String(), nil
}
