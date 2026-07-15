package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// PlanOptions configures a migration plan.
type PlanOptions struct {
	ConfigPath  string
	FactoryPath string // optional; empty defaults to factory.DefaultSettingsPath()
	OldPort     int    // defaults to config.OldDefaultListenPort
	NewPort     int    // defaults to config.DefaultListenPort
}

// Plan describes what migration will do. It is safe to print; it never
// contains file bodies or secrets.
type Plan struct {
	ConfigPath     string
	FactoryPath    string
	ConfigEligible bool
	ConfigReason   string // sanitized reason when not eligible
	Host           string
	OldPort        int
	NewPort        int

	FactoryPresent bool
	FactoryChanges []FactoryEntryChange
	FactoryNoop    bool
	FactoryUnsafe  bool
	FactoryReason  string

	// Internal state for commit (not printed).
	configAnalysis *ConfigAnalysis
	configRaw      []byte
	factoryRaw     []byte
}

// HasChanges reports whether the plan would make any file changes.
func (p *Plan) HasChanges() bool {
	return p.ConfigEligible || len(p.FactoryChanges) > 0
}

// Summary returns a sanitized, human-readable summary of the plan. It never
// includes file bodies, secrets, or full Factory entries.
func (p *Plan) Summary() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("config: %s", p.ConfigPath))
	if p.ConfigEligible {
		lines = append(lines, fmt.Sprintf("  listen.port: %d -> %d", p.OldPort, p.NewPort))
		lines = append(lines, fmt.Sprintf("  listen.host: %s", p.Host))
	} else if p.ConfigReason != "" {
		lines = append(lines, fmt.Sprintf("  no change: %s", p.ConfigReason))
	}

	if p.FactoryPath != "" {
		lines = append(lines, fmt.Sprintf("factory: %s", p.FactoryPath))
		if p.FactoryUnsafe {
			lines = append(lines, fmt.Sprintf("  unsafe: %s", p.FactoryReason))
		} else if len(p.FactoryChanges) > 0 {
			lines = append(lines, fmt.Sprintf("  %d eligible entry/entries", len(p.FactoryChanges)))
			for _, ch := range p.FactoryChanges {
				lines = append(lines, fmt.Sprintf("    model %q: %s -> %s", ch.Model, sanitizeURL(ch.OldOrigin), sanitizeURL(ch.NewOrigin)))
			}
		} else if p.FactoryNoop {
			lines = append(lines, "  no eligible entries")
		} else {
			lines = append(lines, "  absent or empty")
		}
	} else {
		lines = append(lines, "factory: not present")
	}

	return strings.Join(lines, "\n")
}

// PlanMigration analyzes the config and optional Factory settings to produce a
// non-mutating migration plan. If the config is not eligible or the Factory
// state is unsafe, the plan reflects that without error.
func PlanMigration(opts PlanOptions) (*Plan, error) {
	if opts.OldPort == 0 {
		opts.OldPort = config.OldDefaultListenPort
	}
	if opts.NewPort == 0 {
		opts.NewPort = config.DefaultListenPort
	}
	if opts.FactoryPath == "" {
		opts.FactoryPath = defaultFactoryPath()
	}

	plan := &Plan{
		ConfigPath:  opts.ConfigPath,
		FactoryPath: opts.FactoryPath,
		OldPort:     opts.OldPort,
		NewPort:     opts.NewPort,
	}

	// Analyze config.
	cfgAnalysis, err := AnalyzeConfig(opts.ConfigPath, opts.OldPort, opts.NewPort)
	if err != nil {
		return nil, err
	}
	plan.configAnalysis = cfgAnalysis
	plan.ConfigEligible = cfgAnalysis.Eligible
	plan.ConfigReason = cfgAnalysis.Reason
	plan.Host = cfgAnalysis.Host
	if cfgAnalysis.Eligible {
		plan.configRaw = cfgAnalysis.RawConfig
	}

	// Load models from the config for Factory fingerprint matching.
	models, err := loadConfigModels(opts.ConfigPath)
	if err != nil {
		// If we can't load models, we can't match Factory entries. If the
		// config is eligible, this is still fine for a config-only plan.
		models = nil
	}

	// Analyze Factory settings.
	factoryRaw, err := os.ReadFile(opts.FactoryPath)
	if err != nil {
		if os.IsNotExist(err) {
			plan.FactoryPresent = false
			// Absent Factory is fine for config-only migration.
			return plan, nil
		}
		// Unreadable file is unsafe.
		plan.FactoryPresent = true
		plan.FactoryUnsafe = true
		plan.FactoryReason = "Factory settings file is unreadable"
		return plan, nil
	}
	plan.FactoryPresent = true
	plan.factoryRaw = factoryRaw

	// Check Factory file trust.
	trust, err := CheckFileTrust(opts.FactoryPath)
	if err != nil {
		return nil, err
	}
	if !trust.Trusted {
		plan.FactoryUnsafe = true
		plan.FactoryReason = trust.Reason
		return plan, nil
	}

	fa, err := AnalyzeFactory(factoryRaw, models, plan.Host, opts.OldPort, opts.NewPort)
	if err != nil {
		return nil, err
	}
	plan.FactoryChanges = fa.Changes
	plan.FactoryNoop = fa.Noop
	plan.FactoryUnsafe = fa.Unsafe
	plan.FactoryReason = fa.Reason

	return plan, nil
}

// CommitPlan applies the migration plan through the transaction layer: it
// creates immutable backups, writes a durable journal, stages and validates
// outputs, commits targets in order with journaled progress, and marks the
// transaction complete. It preserves file modes and ownership. If the Factory
// state is unsafe, it aborts before writing either target.
//
// Even when the plan has no changes (no-op), a non-dry-run invocation
// acquires the trusted lock and recovers any matching interrupted transaction
// before returning a no-op result.
func CommitPlan(plan *Plan) (*TransactionResult, error) {
	return CommitTransaction(plan, TransactionOptions{})
}

// writeFilePreservingMode writes data to path, preserving the existing file's
// permission mode and ownership.
func writeFilePreservingMode(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	mode := info.Mode().Perm()
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".migration-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// loadConfigModels loads just the models from a config file for Factory
// fingerprint matching. It does not apply defaults or validate.
func loadConfigModels(path string) ([]*config.Model, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mf struct {
		Models []*config.Model `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &mf); err != nil {
		return nil, err
	}
	for _, m := range mf.Models {
		_ = config.HydrateModel(m)
	}
	return mf.Models, nil
}

// defaultFactoryPath returns the canonical Factory settings path.
func defaultFactoryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".factory", "settings.json")
}

// sanitizeURL masks credentials in a URL for safe display. Since migration
// URLs are always loopback http origins, this just returns the URL as-is.
func sanitizeURL(u string) string {
	return u
}
