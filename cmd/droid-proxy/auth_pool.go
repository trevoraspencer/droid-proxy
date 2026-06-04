package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
)

func runAuthPool(args []string) {
	fs := flag.NewFlagSet("auth pool", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	baseURL := fs.String("url", "", "proxy base URL (default from config listen host/port)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "auth pool args error: %v\n", err)
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth pool error: %v\n", err)
		os.Exit(2)
	}
	addr := strings.TrimSpace(*baseURL)
	if addr == "" {
		host := cfg.Listen.Host
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		addr = fmt.Sprintf("http://%s:%d", host, cfg.Listen.Port)
	}
	addr = strings.TrimRight(addr, "/")

	if out, err := fetchPoolHealth(addr, cfg); err == nil {
		fmt.Print(out)
		return
	}

	manager := oauth.NewManager(cfg)
	out, err := formatOfflinePoolHealth(manager)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth pool error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(out)
}

func fetchPoolHealth(baseURL string, cfg *config.Config) (string, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/oauth/pool-health", nil)
	if err != nil {
		return "", err
	}
	if tok := clientAuthToken(cfg); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pool-health returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return formatPoolHealthJSON(raw)
}

func clientAuthToken(cfg *config.Config) string {
	if cfg == nil || !cfg.ClientAuth.Enabled || len(cfg.ClientAuth.APIKeys) == 0 {
		return ""
	}
	return strings.TrimSpace(cfg.ClientAuth.APIKeys[0])
}

func formatPoolHealthJSON(raw []byte) (string, error) {
	var payload struct {
		Strategy          string `json:"strategy"`
		CodexAccountCount int    `json:"codex_account_count"`
		EligibleCount     int    `json:"eligible_count"`
		Affinity          *struct {
			BoundConversations int    `json:"bound_conversations"`
			File               string `json:"file"`
		} `json:"affinity"`
		Accounts []struct {
			Selector               string             `json:"selector"`
			Disabled               bool               `json:"disabled"`
			Healthy                bool               `json:"healthy"`
			InFlight               int                `json:"in_flight"`
			MaxUsedPercent         *float64           `json:"max_used_percent"`
			BoundConversationCount int                `json:"bound_conversation_count"`
			CooldownUntil          *time.Time         `json:"cooldown_until"`
			RateLimitedUntil       *time.Time         `json:"rate_limit_until"`
			Quota                  *oauth.CodexQuota  `json:"quota"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "strategy: %s  accounts: %d  eligible: %d\n",
		payload.Strategy, payload.CodexAccountCount, payload.EligibleCount)
	if payload.Affinity != nil {
		fmt.Fprintf(&b, "affinity: %d bound conversations (%s)\n",
			payload.Affinity.BoundConversations, payload.Affinity.File)
	}
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ACCOUNT\tUSED%\tIN_FLIGHT\tBOUND\tHEALTH\tLIMITED_UNTIL")
	for _, acct := range payload.Accounts {
		used := "-"
		if acct.MaxUsedPercent != nil {
			used = fmt.Sprintf("%.0f", *acct.MaxUsedPercent)
		}
		limited := "-"
		if acct.RateLimitedUntil != nil {
			limited = acct.RateLimitedUntil.Format(time.RFC3339)
		} else if acct.CooldownUntil != nil {
			limited = "cooldown:" + acct.CooldownUntil.Format(time.RFC3339)
		}
		health := "ok"
		if !acct.Healthy {
			health = "unhealthy"
		}
		if acct.Disabled {
			health = "disabled"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n",
			acct.Selector, used, acct.InFlight, acct.BoundConversationCount, health, limited)
	}
	_ = w.Flush()
	return b.String(), nil
}

func formatOfflinePoolHealth(manager *oauth.Manager) (string, error) {
	authDir, err := manager.AuthDir()
	if err != nil {
		return "", err
	}
	tokens, err := manager.LoadTokens(oauth.ProviderCodex)
	if err != nil {
		return "", err
	}
	pool := oauth.NewAccountPool(tokens, time.Now, config.LoadBalancing{Strategy: config.LoadBalancingSticky}, nil)
	snap := pool.Snapshot()
	raw, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	out, err := formatPoolHealthJSON(raw)
	if err != nil {
		return "", err
	}
	return out + fmt.Sprintf("\n(offline snapshot from %s; start proxy for live in-flight/cooldown state)\n", authDir), nil
}