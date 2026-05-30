package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"droid-proxy/internal/config"
	"droid-proxy/internal/configedit"
	"droid-proxy/internal/daemon"
	"droid-proxy/internal/factory"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/providerapi"
	"droid-proxy/internal/secrets"
)

// backend wires the config editor, secrets store, Factory writer, model
// discovery, OAuth manager, and daemon controls together for the TUI. It holds
// no UI state.
type backend struct {
	configPath  string
	factoryPath string
	baseURL     string
	factoryKey  string
	manager     *oauth.Manager
}

func newBackend(configPath string) *backend {
	cfg := loadConfigBestEffort(configPath)
	return &backend{
		configPath:  configPath,
		factoryPath: factory.DefaultSettingsPath(),
		baseURL:     proxyBaseURL(configPath),
		factoryKey:  factoryAPIKey(cfg),
		manager:     oauth.NewManager(cfg),
	}
}

// factoryAPIKey returns the API key Droid should send to the proxy. When the
// proxy enforces client_auth, the first configured (env-expanded) key is used
// so synced Factory entries authenticate; otherwise a placeholder is returned.
func factoryAPIKey(cfg *config.Config) string {
	if cfg != nil && cfg.ClientAuth.Enabled {
		for _, k := range cfg.ClientAuth.APIKeys {
			if strings.TrimSpace(k) != "" {
				return k
			}
		}
	}
	return "x"
}

// models returns the configured models (hydrated, no env validation).
func (b *backend) models() ([]*config.Model, error) {
	return configedit.LoadModels(b.configPath)
}

// keySet reports whether the model's API key env var is populated in the
// process environment (which includes the managed secrets file and .env.local
// loaded at startup).
func (b *backend) keySet(m *config.Model) bool {
	env := strings.TrimSpace(m.APIKeyEnv)
	if env == "" {
		return true
	}
	return strings.TrimSpace(os.Getenv(env)) != ""
}

// setKey writes the key to the managed secrets file and the live process env.
func (b *backend) setKey(envVar, value string) error {
	envVar = strings.TrimSpace(envVar)
	if envVar == "" {
		return fmt.Errorf("no API key env var for this provider")
	}
	if err := secrets.Set(envVar, value); err != nil {
		return err
	}
	return os.Setenv(envVar, value)
}

// addModel writes (or replaces) the model in the config file.
func (b *backend) addModel(m *config.Model) error {
	doc, err := configedit.Load(b.configPath)
	if err != nil {
		return err
	}
	if err := doc.Upsert(m); err != nil {
		return err
	}
	return doc.Save()
}

// removeModel deletes the model from the config and (if present) Factory.
func (b *backend) removeModel(alias string) error {
	doc, err := configedit.Load(b.configPath)
	if err != nil {
		return err
	}
	if _, err := doc.Remove(alias); err != nil {
		return err
	}
	if err := doc.Save(); err != nil {
		return err
	}
	settings, err := factory.Load(b.factoryPath)
	if err != nil {
		return err
	}
	removed, err := settings.Remove(alias)
	if err != nil {
		return err
	}
	if removed {
		return settings.Save(true)
	}
	return nil
}

// factoryModels returns the set of model aliases present in settings.json.
func (b *backend) factoryModels() map[string]bool {
	out := map[string]bool{}
	settings, err := factory.Load(b.factoryPath)
	if err != nil {
		return out
	}
	names, err := settings.Models()
	if err != nil {
		return out
	}
	for _, n := range names {
		out[n] = true
	}
	return out
}

// syncFactory upserts the given models into settings.json.
func (b *backend) syncFactory(models []*config.Model) error {
	settings, err := factory.Load(b.factoryPath)
	if err != nil {
		return err
	}
	for _, m := range models {
		if err := settings.Upsert(factory.EntryFromModel(m, b.baseURL, b.factoryKey)); err != nil {
			return err
		}
	}
	return settings.Save(true)
}

// discover queries the provider's /models endpoint for the given model's
// upstream. Only meaningful for OpenAI-compatible upstreams.
func (b *backend) discover(ka config.KnownAuth, baseURL, apiKey string) ([]string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = ka.BaseURL
	}
	return providerapi.ListModels(context.Background(), baseURL, apiKey, ka.AuthHeader, ka.AuthScheme)
}

// oauthAccounts returns saved tokens for a provider.
func (b *backend) oauthAccounts(provider config.OAuthProvider) ([]*oauth.Token, error) {
	return b.manager.LoadTokens(provider)
}

// oauthHealth returns the number of active (enabled, not-expiring) accounts
// matching the model's pinned account, and the total matching accounts.
func (b *backend) oauthHealth(m *config.Model) (active, total int) {
	tokens, err := b.manager.LoadTokens(m.OAuthProvider)
	if err != nil {
		return 0, 0
	}
	now := time.Now()
	for _, t := range tokens {
		if !t.MatchesAccount(m.OAuthAccount) {
			continue
		}
		total++
		if !t.Disabled && !t.NeedsRefresh(now) {
			active++
		}
	}
	return active, total
}

func (b *backend) setOAuthDisabled(provider config.OAuthProvider, account string, disabled bool) error {
	_, err := b.manager.SetTokenDisabled(provider, account, disabled)
	return err
}

func (b *backend) oauthLogout(provider config.OAuthProvider, account string) error {
	_, err := b.manager.DeleteToken(provider, account)
	return err
}

// proxyRunning reports whether the daemon is up.
func (b *backend) proxyRunning() bool {
	_, running := daemon.IsRunning()
	return running
}

// restartProxy stops any running daemon and spawns a fresh detached one.
func (b *backend) restartProxy() error {
	_ = daemon.Stop()
	daemon.CleanStalePID()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(exe, "start", "--config", b.configPath)
	child.Env = os.Environ()
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	for i := 0; i < 30; i++ {
		if _, running := daemon.IsRunning(); running {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for proxy to start")
}

func proxyBaseURL(configPath string) string {
	var lf struct {
		Listen struct {
			Host string `yaml:"host"`
			Port int    `yaml:"port"`
		} `yaml:"listen"`
	}
	if data, err := os.ReadFile(configPath); err == nil {
		_ = yaml.Unmarshal(data, &lf)
	}
	host := lf.Listen.Host
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	port := lf.Listen.Port
	if port == 0 {
		port = 8787
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func loadConfigBestEffort(path string) *config.Config {
	if cfg, err := config.Load(path); err == nil {
		return cfg
	}
	var partial config.Config
	if data, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(data, &partial)
	}
	return &partial
}
