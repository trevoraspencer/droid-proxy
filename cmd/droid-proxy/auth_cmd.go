package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
)

func runAuth(args []string) {
	if len(args) == 0 {
		printAuthUsage(os.Stderr)
		os.Exit(2)
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printAuthUsage(os.Stdout)
		return
	case "status":
		runAuthStatus(args[1:])
		return
	case "pool":
		runAuthPool(args[1:])
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
	loadConfigEnv(*configPath)
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

func printAuthUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: droid-proxy auth <codex|xai> [--device] --config config.yaml")
	fmt.Fprintln(out, "       droid-proxy auth status [codex|xai] --config config.yaml")
	fmt.Fprintln(out, "       droid-proxy auth pool [--url http://127.0.0.1:PORT] --config config.yaml")
	fmt.Fprintln(out, "       droid-proxy auth <enable|disable|logout> <provider> <account> --config config.yaml")
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
	loadConfigEnv(configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return oauth.NewManager(cfg), nil
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
