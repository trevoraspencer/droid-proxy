package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/trevoraspencer/droid-proxy/internal/tui"
)

func runConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	_ = fs.Parse(args)
	loadConfigEnv(*configPath)
	if err := tui.Run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy config error: %v\n", err)
		os.Exit(1)
	}
}
