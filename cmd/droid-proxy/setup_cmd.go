package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	"github.com/trevoraspencer/droid-proxy/internal/setup"
)

func runSetup(args []string) {
	defaultConfig, err := setup.DefaultConfigPath()
	if err != nil {
		defaultConfig = ""
	}
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	configPath := fs.String("config", defaultConfig, "path to per-user config.yaml")
	installService := fs.Bool("service", false, "install and start the per-user service")
	_ = fs.Parse(args)

	res, err := setup.EnsureConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy setup error: %v\n", err)
		os.Exit(1)
	}
	if res.Created {
		fmt.Printf("created config: %s\n", res.Path)
	} else {
		fmt.Printf("config exists: %s\n", res.Path)
	}
	if *installService {
		if err := validateSetupServiceConfig(res.Path); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy setup error: %v\n", err)
			os.Exit(1)
		}
		if err := daemon.InstallService(res.Path); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy setup error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("service installed: %s\n", daemon.ServiceDescription())
		fmt.Printf("logs: %s/stdout.log, %s/stderr.log\n", daemon.StateDir(), daemon.StateDir())
		return
	}
	fmt.Printf("next: droid-proxy config --config %q\n", res.Path)
	fmt.Printf("service: droid-proxy setup --service --config %q\n", res.Path)
}

func validateSetupServiceConfig(configPath string) error {
	loadConfigEnv(configPath)
	if _, err := config.Load(configPath); err != nil {
		return fmt.Errorf("config is not ready to run: %w\nrun droid-proxy config --config %q first", err, configPath)
	}
	return nil
}
