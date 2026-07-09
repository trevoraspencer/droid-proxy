package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/daemon"
)

type serviceRunConfig struct {
	ConfigPath string
	EnvFile    string
}

func validateServiceInstallConfig(configPath string) error {
	workDir := configWorkDir(configPath)
	return validateRunnableConfig(configPath, daemon.ResolveExistingEnvFile(workDir))
}

func validateRunnableConfig(configPath, envFile string) error {
	restoreEnv := snapshotProcessEnv()
	defer restoreEnv()
	prepareServiceValidationEnv()

	if err := daemon.LoadLayeredEnv(configWorkDir(configPath), envFile); err != nil {
		return fmt.Errorf("env file: %w", err)
	}
	if _, err := config.Load(configPath); err != nil {
		return fmt.Errorf("config is not ready to run: %w\nrun droid-proxy config --config %q first", err, configPath)
	}
	return nil
}

func doctorServiceConfigIssues(args []string) []string {
	runConfig, ok := serviceRunConfigFromArgs(args)
	if !ok {
		return nil
	}
	if _, err := os.Stat(runConfig.ConfigPath); os.IsNotExist(err) {
		// serviceArgumentIssues already reports missing config paths.
		return nil
	}
	if err := validateRunnableConfig(runConfig.ConfigPath, runConfig.EnvFile); err != nil {
		return []string{"service config is not runnable: " + err.Error()}
	}
	return nil
}

func serviceRunConfigFromArgs(args []string) (serviceRunConfig, bool) {
	var out serviceRunConfig
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 < len(args) {
				out.ConfigPath = args[i+1]
				i++
			}
		case "--env-file":
			if i+1 < len(args) {
				out.EnvFile = args[i+1]
				i++
			}
		}
	}
	return out, strings.TrimSpace(out.ConfigPath) != ""
}

func snapshotProcessEnv() func() {
	entries := os.Environ()
	return func() {
		os.Clearenv()
		for _, entry := range entries {
			key, value, ok := strings.Cut(entry, "=")
			if !ok {
				continue
			}
			_ = os.Setenv(key, value)
		}
	}
}

func prepareServiceValidationEnv() {
	preserved := map[string]string{}
	for _, key := range []string{
		"HOME",
		"LOGNAME",
		"TMPDIR",
		"USER",
		"XDG_CONFIG_HOME",
		"XDG_RUNTIME_DIR",
	} {
		if value, ok := os.LookupEnv(key); ok {
			preserved[key] = value
		}
	}
	os.Clearenv()
	for key, value := range preserved {
		_ = os.Setenv(key, value)
	}
}
