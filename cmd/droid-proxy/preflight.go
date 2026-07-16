package main

import (
	"fmt"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
)

// omittedPortPreflight checks the selected canonical Factory settings file
// when the config's listen.port is omitted and would resolve to the new
// default (9787). If an exact high-confidence Factory entry still targets the
// old loopback origin and no trusted transaction resolves it, the preflight
// refuses before the server binds a listener.
//
// This is a read-only check: it never writes settings, creates migration
// state, scans alternate Factory roots, or selects another Factory file.
//
// The automatic-migration opt-out (--no-migrate-port) does not disable this
// preflight.
func omittedPortPreflight(cfg *config.Config) error {
	if !cfg.PortOmitted() {
		return nil
	}

	factoryPath := factory.DefaultSettingsPath()
	result, err := factory.CoherencePreflight(cfg.Listen.Host, cfg.Models, factoryPath)
	if err != nil {
		return fmt.Errorf("startup preflight error: %w", err)
	}
	if !result.Allowed {
		return fmt.Errorf("startup preflight refused: %s", result.Reason)
	}
	return nil
}
