package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	"github.com/trevoraspencer/droid-proxy/internal/migration"
)

// attemptManagedMigration checks for trusted deferred provenance and performs
// automatic migration if eligible. It is called by verified controlled
// restart paths: CLI restart, update/installer restart, and (via delegation)
// TUI 'r'.
//
// If noMigratePort is true, automatic migration is skipped for this
// invocation but the omitted-port startup preflight remains enforced.
//
// This function never starts or stops services; it only performs the
// file-level migration. The caller is responsible for restarting the service
// after migration.
func attemptManagedMigration(configPath string, noMigratePort bool) {
	exe, err := currentExecutablePath()
	if err != nil {
		return // cannot verify provenance without executable path
	}

	result, err := migration.AttemptDeferredMigration(migration.ManagedRestartOptions{
		ConfigPath:          absPathOrOriginal(configPath),
		InstalledBinaryPath: exe,
		NoMigratePort:       noMigratePort,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy: automatic migration warning: %v\n", err)
		return
	}

	switch result.Action {
	case "migrated":
		fmt.Printf("automatic port migration completed (listen.port 8787 -> 9787).\n")
		if result.Result != nil {
			fmt.Printf("  transaction: %s\n", result.Result.ID)
		}
	case "skipped":
		if result.Reason != "" {
			fmt.Printf("automatic port migration skipped: %s\n", result.Reason)
		}
	case "ineligible":
		// Config is not an explicit old-default; no action needed.
	case "no-provenance":
		// No deferred upgrade provenance; normal restart.
	}
}

// recordUpdateProvenance creates a deferred provenance record after a
// successful binary-only upgrade (update --no-restart). It captures the
// trusted tuple so the next verified controlled restart can perform the
// deferred migration.
func recordUpdateProvenance(binaryPath, oldBinaryHash, configPath string) {
	if oldBinaryHash == "" {
		return
	}

	newBinaryHash := migration.ReadBinaryHashForProvenance(binaryPath)
	if newBinaryHash == "" || newBinaryHash == oldBinaryHash {
		// Binary not actually replaced or unreadable.
		return
	}

	configHash := migration.ReadConfigHashForProvenance(configPath)
	if configHash == "" {
		return
	}

	serviceKind := resolveCurrentServiceKind()
	if serviceKind == "" {
		return
	}

	var daemonPID int
	var daemonExe string
	if meta, err := daemon.ReadRuntimeMetadata(); err == nil {
		daemonPID = meta.PID
		daemonExe = meta.Executable
	}

	if err := migration.RecordDeferredProvenance(
		migration.StateRoot(),
		binaryPath, oldBinaryHash, "",
		binaryPath, newBinaryHash, "",
		configPath, configHash,
		serviceKind, "", "",
		daemonPID, daemonExe,
	); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy: could not record deferred migration provenance: %v\n", err)
		return
	}

	fmt.Println("deferred port-migration provenance recorded.")
	fmt.Println("The running service was not restarted. To complete the migration:")
	fmt.Printf("  1. Run 'droid-proxy restart' to perform the deferred migration and restart, or\n")
	fmt.Printf("  2. Run 'droid-proxy migrate-port --config %s' for explicit migration.\n", configPath)
}

// resolveCurrentServiceKind determines the service kind for the current
// installation.
func resolveCurrentServiceKind() string {
	if daemon.ServiceInstalled() {
		if runtime.GOOS == "linux" {
			return "systemd"
		}
		return "launchd"
	}
	if _, running := daemon.IsRunning(); running {
		return "background-daemon"
	}
	return ""
}

// configUsesThisConfig checks whether a running daemon or managed service
// uses the given config path. Returns the PID and true if so.
func configUsesThisConfig(configPath string) (int, bool) {
	absConfig := absPathOrOriginal(configPath)

	// Check background daemon via runtime metadata.
	if meta, err := daemon.ReadRuntimeMetadata(); err == nil {
		if meta.ConfigPath == absConfig {
			if _, running := daemon.IsRunning(); running {
				return meta.PID, true
			}
		}
	}

	// Check managed service: verify the service definition references this
	// config path and the service is running.
	if st := daemon.ServiceRunning(); st.Installed && st.Running {
		if check, err := daemon.CheckService(); err == nil && check.Installed {
			for i, arg := range check.ProgramArguments {
				if arg == "--config" && i+1 < len(check.ProgramArguments) {
					svcConfig := absPathOrOriginal(check.ProgramArguments[i+1])
					if svcConfig == absConfig {
						return st.PID, true
					}
				}
			}
		}
	}

	return 0, false
}
