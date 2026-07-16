package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/configedit"
	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
	"github.com/trevoraspencer/droid-proxy/internal/migration"
	"github.com/trevoraspencer/droid-proxy/internal/oauth"
	"github.com/trevoraspencer/droid-proxy/internal/providerapi"
	"github.com/trevoraspencer/droid-proxy/internal/secrets"
)

// backend wires the config editor, secrets store, Factory writer, model
// discovery, OAuth manager, and daemon controls together for the TUI. It holds
// no UI state.
type backend struct {
	configPath       string
	factoryPath      string
	baseURL          string
	portIsZero       bool // true when listen.port is explicitly 0
	factoryKey       string
	manager          *oauth.Manager
	migrationMessage string // sanitized message from the last restart migration
}

func newBackend(configPath string) *backend {
	cfg := loadConfigBestEffort(configPath)
	url, portIsZero := resolveListenURL(configPath, cfg)
	return &backend{
		configPath:  configPath,
		factoryPath: factory.DefaultSettingsPath(),
		baseURL:     url,
		portIsZero:  portIsZero,
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

// addModel writes a new model to the config file. Existing aliases are never
// replaced by the add flow: presets use public-looking aliases, so an implicit
// upsert could otherwise destroy a user's unrelated model configuration.
func (b *backend) addModel(m *config.Model) error {
	doc, err := configedit.Load(b.configPath)
	if err != nil {
		return err
	}
	if m != nil && doc.HasModel(m.Alias) {
		return fmt.Errorf("model alias %q already exists; choose a different alias or remove the existing model first", strings.TrimSpace(m.Alias))
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
	if b.portIsZero {
		return fmt.Errorf("cannot sync to Factory: listen.port is explicitly 0; set a stable explicit port in the config before syncing")
	}
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

// discover queries the provider's model-list endpoint for the selected profile.
func (b *backend) discover(ka config.KnownAuth, baseURL, apiKey string) ([]string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = ka.BaseURL
	}
	return providerapi.ListModelsWithOptions(context.Background(), providerapi.ListOptions{
		BaseURL:      baseURL,
		ModelsPath:   ka.ModelsPath,
		APIKey:       apiKey,
		AuthHeader:   ka.AuthHeader,
		AuthScheme:   ka.AuthScheme,
		ExtraHeaders: ka.ExtraHeaders,
	})
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

// Seams for daemon/service state so tests never touch the real launchd or
// pidfile (same pattern as launchAgentLoader in internal/daemon).
var (
	serviceInstalled = daemon.ServiceInstalled
	restartService   = daemon.RestartService
	serviceRunning   = daemon.ServiceRunning
	daemonIsRunning  = daemon.IsRunning
)

// proxyRunning reports whether the proxy is up, either as a pidfile daemon or
// under the managed launchd/systemd service.
func (b *backend) proxyRunning() bool {
	if _, running := daemonIsRunning(); running {
		return true
	}
	return serviceRunning().Running
}

// restartHint nudges the user to restart after config edits that the running
// proxy has not applied. Empty when nothing is running.
func (b *backend) restartHint() string {
	if !b.proxyRunning() {
		return ""
	}
	return " Restart the proxy (r on the dashboard) to apply."
}

// consumeMigrationMessage returns and clears the sanitized migration message
// from the last restartProxy call. Returns empty when no migration occurred.
func (b *backend) consumeMigrationMessage() string {
	msg := b.migrationMessage
	b.migrationMessage = ""
	return msg
}

// attemptDeferredMigration checks for trusted deferred upgrade provenance
// and performs automatic migration if eligible. This is the verified
// controlled-restart integration point for TUI 'r'. It returns a sanitized
// message describing the migration outcome, or empty if no migration
// occurred. Production managed restart paths must hold destination
// protection through the coherent restart rather than passing a nil checker.
func (b *backend) attemptDeferredMigration() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}

	// Plan the migration first to determine eligibility and the
	// destination port.
	plan, err := migration.PlanMigration(migration.PlanOptions{
		ConfigPath: b.configPath,
	})
	if err != nil {
		return ""
	}

	var reservation *migration.DestinationReservation
	if plan.ConfigEligible && !plan.FactoryUnsafe && plan.HasChanges() {
		// Reserve the destination port and hold it through the restart
		// transition. A nil or transient check is not acceptable.
		reservation, err = migration.ReserveDestination(plan.Host, plan.NewPort)
		if err != nil {
			return fmt.Sprintf("automatic migration refused: %v", err)
		}
		defer reservation.Close()
	}

	opts := migration.ManagedRestartOptions{
		ConfigPath:          b.configPath,
		InstalledBinaryPath: exe,
	}
	if reservation != nil {
		opts.DestinationChecker = reservation.HeldChecker()
	}

	result, err := migration.AttemptDeferredMigration(opts)
	if err != nil {
		return fmt.Sprintf("automatic migration warning: %v", err)
	}

	switch result.Action {
	case "migrated":
		msg := "automatic port migration completed (listen.port 8787 -> 9787)."
		if result.Result != nil {
			msg += fmt.Sprintf(" transaction: %s", result.Result.ID)
		}
		return msg
	case "skipped":
		if result.Reason != "" {
			return fmt.Sprintf("automatic port migration skipped: %s", result.Reason)
		}
		return ""
	default:
		return ""
	}
}

// restartProxy restarts the proxy. With a managed service installed it goes
// through the service manager — stopping the process directly would only
// fight KeepAlive/Restart=always and race a second daemon onto the port.
// TUI 'r' delegates to the verified controlled-restart path: it checks for
// deferred upgrade provenance and performs automatic migration before
// restarting. Migration results are propagated as actionable messages.
func (b *backend) restartProxy() error {
	// Verified controlled restart: check for deferred provenance and
	// perform automatic migration if eligible. This is the only path
	// through which automatic migration runs. The result message is
	// propagated so TUI restart failures are actionable.
	migrationMsg := b.attemptDeferredMigration()
	if migrationMsg != "" {
		b.migrationMessage = migrationMsg
	} else {
		b.migrationMessage = ""
	}

	if serviceInstalled() {
		if err := restartService(); err != nil {
			return err
		}
		for i := 0; i < 30; i++ {
			if b.proxyRunning() {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("timed out waiting for the managed service to report running")
	}
	_ = daemon.Stop()
	daemon.CleanStalePID()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"start", "--config", b.configPath}
	if envFile := daemon.RuntimeEnvFileForConfig(b.configPath); envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	child := exec.Command(exe, args...)
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

// resolveListenURL computes the proxy's listen URL for Factory sync. It
// distinguishes an omitted port (defaults to config.DefaultListenPort) from
// an explicit port 0 (returns an empty URL and portIsZero=true so the caller
// can refuse the sync).
func resolveListenURL(configPath string, cfg *config.Config) (url string, portIsZero bool) {
	// When a full config load succeeded, presence tracking is reliable.
	if cfg != nil && cfg.HasPresence() {
		if cfg.PortExplicitlyZero() {
			return "", true
		}
		if cfg.PortOmitted() {
			return config.FormatListenURL(cfg.Listen.Host, config.DefaultListenPort), false
		}
		return config.FormatListenURL(cfg.Listen.Host, cfg.Listen.Port), false
	}
	// Best-effort fallback: parse just the listen block with presence tracking.
	host, port, portPresent := parseListenBlock(configPath)
	if portPresent && port == 0 {
		return "", true
	}
	if port == 0 {
		port = config.DefaultListenPort
	}
	return config.FormatListenURL(host, port), false
}

// parseListenBlock reads a config file and extracts listen.host and listen.port
// with presence tracking for the port field. It never fails; missing or
// malformed values default to zero values.
func parseListenBlock(configPath string) (host string, port int, portPresent bool) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", 0, false
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return "", 0, false
	}
	n := &root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return "", 0, false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "listen" {
			continue
		}
		listenNode := n.Content[i+1]
		if listenNode.Kind != yaml.MappingNode {
			break
		}
		for j := 0; j+1 < len(listenNode.Content); j += 2 {
			key := listenNode.Content[j].Value
			val := listenNode.Content[j+1]
			switch key {
			case "host":
				host = val.Value
			case "port":
				portPresent = true
				var p int
				if val.Kind == yaml.ScalarNode {
					_ = yaml.Unmarshal([]byte(val.Value), &p)
				}
				port = p
			}
		}
		break
	}
	return host, port, portPresent
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
