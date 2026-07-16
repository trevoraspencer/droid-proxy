package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/trevoraspencer/droid-proxy/internal/migration"
)

func runMigratePort(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printMigratePortUsage(os.Stdout)
		return
	}

	// Check for rollback mode.
	isRollback := false
	remaining := args
	if args[0] == "--rollback" || args[0] == "-rollback" {
		isRollback = true
		remaining = args[1:]
	}

	fs := flag.NewFlagSet("migrate-port", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml (defaults to the per-user config)")
	dryRun := fs.Bool("dry-run", false, "report the migration plan without writing any files")
	_ = fs.Parse(remaining)

	if isRollback {
		runMigratePortRollback(*configPath)
		return
	}

	runMigratePortCommit(*configPath, *dryRun)
}

func runMigratePortCommit(configPath string, dryRun bool) {
	if configPath == "" {
		configPath = perUserConfigPath()
		if configPath == "" {
			fmt.Fprintln(os.Stderr, "droid-proxy migrate-port error: cannot determine per-user config path; pass --config <path>")
			os.Exit(2)
		}
	}

	plan, err := migration.PlanMigration(migration.PlanOptions{
		ConfigPath: configPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy migrate-port error: %v\n", err)
		os.Exit(1)
	}

	// Report the plan.
	fmt.Print(plan.Summary())
	fmt.Println()

	if plan.FactoryUnsafe {
		fmt.Fprintf(os.Stderr, "droid-proxy migrate-port: refusing due to unsafe Factory state: %s\n", plan.FactoryReason)
		os.Exit(1)
	}

	if dryRun {
		// Dry-run never mutates recovery state or files.
		if !plan.HasChanges() {
			if !plan.ConfigEligible && plan.ConfigReason != "" {
				fmt.Printf("no eligible migration: %s\n", plan.ConfigReason)
			} else {
				fmt.Println("no eligible migration changes.")
			}
		}
		fmt.Println("dry-run: no files written.")
		return
	}

	// If a running droid-proxy daemon or managed service uses this config,
	// refuse before mutation with stop-and-retry guidance. Explicit migration
	// must never commit files while leaving the runtime on the old port.
	if plan.ConfigEligible {
		if pid, inUse := configUsesThisConfig(configPath); inUse {
			fmt.Fprintf(os.Stderr, "droid-proxy migrate-port: refusing because a running proxy (pid %d) uses this config.\n", pid)
			fmt.Fprintf(os.Stderr, "  Stop it first with 'droid-proxy stop', then retry:\n")
			fmt.Fprintf(os.Stderr, "    droid-proxy migrate-port --config %s\n", configPath)
			fmt.Fprintf(os.Stderr, "  Then restart: droid-proxy start --config %s\n", configPath)
			os.Exit(1)
		}
	}

	// Non-dry-run: always acquire the trusted lock. Even when the plan is a
	// no-op (config already at the new port), this recovers and finalizes a
	// matching interrupted transaction whose targets are already all-new.
	result, err := migration.CommitPlan(plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy migrate-port error: %v\n", err)
		os.Exit(1)
	}

	switch result.Action {
	case "migrated":
		fmt.Printf("migration complete: listen.port %d -> %d\n", plan.OldPort, plan.NewPort)
		if len(plan.FactoryChanges) > 0 {
			fmt.Printf("updated %d Factory entry/entries\n", len(plan.FactoryChanges))
		}
	case "recovered":
		fmt.Printf("recovered interrupted migration transaction %s: listen.port %d -> %d\n", result.ID, plan.OldPort, plan.NewPort)
	default:
		if !plan.ConfigEligible && plan.ConfigReason != "" {
			fmt.Printf("no eligible migration: %s\n", plan.ConfigReason)
		} else {
			fmt.Println("no eligible migration changes.")
		}
	}
}

func runMigratePortRollback(configPath string) {
	// Rollback requires a completed transaction from the journal. The
	// transaction layer is provided by the migration transaction feature.
	// For now, we look for transactions and report zero candidates.
	transactions, err := migration.FindRollbackCandidates(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy migrate-port rollback error: %v\n", err)
		os.Exit(1)
	}

	if len(transactions) == 0 {
		fmt.Println("no completed migration transactions found to roll back.")
		os.Exit(0)
	}

	if configPath == "" && len(transactions) > 1 {
		fmt.Println("multiple completed migration transactions found; specify --config <path> to select one:")
		for _, t := range transactions {
			fmt.Printf("  config: %s\n", t.ConfigPath)
		}
		os.Exit(1)
	}

	// Delegate to the transaction layer for actual rollback.
	if err := migration.RollbackTransaction(transactions[0]); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy migrate-port rollback error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("rollback complete.")
}

func printMigratePortUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: droid-proxy migrate-port [options]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Migrate the listen.port from the old default (8787) to the new default (9787)")
	fmt.Fprintln(out, "in an eligible config file and its high-confidence Factory customModels entries.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Command forms:")
	fmt.Fprintln(out, "  droid-proxy migrate-port --dry-run --config <path>")
	fmt.Fprintln(out, "  droid-proxy migrate-port --config <path>")
	fmt.Fprintln(out, "  droid-proxy migrate-port --rollback [--config <path>]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Explicit migration considers only the isolated HOME's default Factory settings")
	fmt.Fprintln(out, "when present and otherwise performs a config-only migration. No alternate Factory")
	fmt.Fprintln(out, "file is inferred or scanned.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Options:")
	fmt.Fprintln(out, "  --dry-run           Report the migration plan without writing any files or starting processes.")
	fmt.Fprintln(out, "  --config <path>     Path to the config.yaml to migrate. A mutating migration without")
	fmt.Fprintln(out, "                      --config resolves only the isolated HOME's canonical per-user config")
	fmt.Fprintln(out, "                      and never a CWD, executable-adjacent, or stale runtime-metadata fallback.")
	fmt.Fprintln(out, "  --rollback          Roll back the latest completed migration transaction.")
	fmt.Fprintln(out, "                      With --config <path>, selects the latest transaction for that config.")
	fmt.Fprintln(out, "                      Without --config, accepted only when exactly one eligible transaction exists.")
	fmt.Fprintln(out, "                      Zero or multiple candidates refuse with sanitized candidate config paths.")
	fmt.Fprintln(out, "  -h, --help          Show this help.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Automatic migration opt-out:")
	fmt.Fprintln(out, "  The managed installer/update/restart commands accept --no-migrate-port, which")
	fmt.Fprintln(out, "  takes no value and disables automatic migration for that invocation only.")
	fmt.Fprintln(out, "  Explicit 'migrate-port' remains an intentional override. The read-only")
	fmt.Fprintln(out, "  omitted-port startup preflight is always enforced regardless of this opt-out.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Restart path classification:")
	fmt.Fprintln(out, "  CLI restart, TUI 'r', background-daemon restart, launchd/systemd restart, and")
	fmt.Fprintln(out, "  installer restart all migrate only through the verified controlled-restart path.")
	fmt.Fprintln(out, "  Setup/service installation cannot infer upgrade provenance. The installer")
	fmt.Fprintln(out, "  --no-service flag suppresses service installation only without broadening")
	fmt.Fprintln(out, "  migration or restart eligibility.")
}
