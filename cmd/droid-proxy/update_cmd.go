package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	"github.com/trevoraspencer/droid-proxy/internal/migration"
	updater "github.com/trevoraspencer/droid-proxy/internal/update"
)

func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	repoPath := fs.String("repo", "", "path to droid-proxy source checkout")
	remote := fs.String("remote", updater.DefaultRemote, "git remote to fetch")
	branch := fs.String("branch", updater.DefaultBranch, "git branch to update from")
	binaryPath := fs.String("binary", "", "path to droid-proxy binary to replace")
	noRestart := fs.Bool("no-restart", false, "do not restart a running proxy after updating")
	noMigratePort := fs.Bool("no-migrate-port", false, "do not perform automatic port migration for this invocation")
	dryRun := fs.Bool("dry-run", false, "print planned update actions without changing files")
	_ = fs.Parse(args)

	exe, err := currentExecutablePath()
	if err != nil && *binaryPath == "" {
		fmt.Fprintf(os.Stderr, "droid-proxy update error: %v\n", err)
		os.Exit(2)
	}
	wd, _ := os.Getwd()
	repo, err := updater.ResolveRepoPath(*repoPath, wd, exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy update error: %v\n", err)
		os.Exit(2)
	}
	binary, err := updater.ResolveBinaryPath(*binaryPath, exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy update error: %v\n", err)
		os.Exit(2)
	}

	pid, running := daemon.IsRunning()
	meta, haveMeta := daemon.RuntimeMetadata{}, false
	if running {
		if m, err := daemon.ReadRuntimeMetadata(); err == nil {
			meta = m
			haveMeta = true
		}
	}

	// Capture the old binary hash before the update replaces it, for
	// deferred provenance recording on the --no-restart path.
	oldBinaryHash := ""
	if !*dryRun {
		oldBinaryHash = readBinaryHashQuietly(binary)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, err := updater.Run(ctx, updater.Options{
		RepoPath:   repo,
		Remote:     *remote,
		Branch:     *branch,
		BinaryPath: binary,
		DryRun:     *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy update error: %v\n", err)
		os.Exit(1)
	}
	printUpdateResult(res, running, pid, !*noRestart)
	if *dryRun {
		if *noMigratePort {
			fmt.Println("automatic port migration would be skipped (--no-migrate-port).")
		}
		return
	}
	if *noMigratePort {
		fmt.Println("automatic port migration skipped for this invocation (--no-migrate-port).")
	}
	if *noRestart {
		// Binary-only upgrade: record deferred provenance so the next
		// verified controlled restart can perform the deferred migration.
		// The old service keeps running on its current port.
		if res.Built && oldBinaryHash != "" {
			configForProvenance := defaultConfigPath()
			if haveMeta && meta.ConfigPath != "" {
				configForProvenance = meta.ConfigPath
			}
			recordUpdateProvenance(binary, oldBinaryHash, configForProvenance)
		}
	} else if running {
		// Direct restart path (pre-migration source updater behavior):
		// check for deferred provenance from a prior --no-restart upgrade
		// before restarting. This is a verified controlled restart.
		configForMigration := defaultConfigPath()
		if haveMeta && meta.ConfigPath != "" {
			configForMigration = meta.ConfigPath
		}
		attemptManagedMigration(configForMigration, *noMigratePort)
		if err := restartAfterUpdate(binary, meta, haveMeta); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy restart error: %v\n", err)
			os.Exit(1)
		}
	}
}

func printUpdateResult(res updater.Result, running bool, pid int, willRestart bool) {
	if res.DryRun {
		fmt.Printf("repo: %s\n", res.RepoPath)
		fmt.Printf("remote: %s (%s)\n", res.Remote, res.RemoteURL)
		fmt.Printf("branch: %s\n", res.Branch)
		fmt.Printf("binary: %s\n", res.BinaryPath)
		fmt.Println("plan:")
		fmt.Printf(" - verify the worktree is clean and has no local-only commits\n")
		fmt.Printf(" - fetch %s %s without force-resetting local work\n", res.Remote, res.Branch)
		fmt.Printf(" - fast-forward only if %s/%s is ahead\n", res.Remote, res.Branch)
		fmt.Printf(" - rebuild droid-proxy and replace %s\n", res.BinaryPath)
		if running && willRestart {
			fmt.Printf(" - restart running proxy pid %d after a successful build\n", pid)
		} else if running {
			fmt.Printf(" - leave running proxy pid %d alone because --no-restart was set\n", pid)
		}
		if res.WorktreeDirty {
			fmt.Println("note: actual update would stop now because the worktree has local changes.")
		}
		return
	}
	if res.Updated {
		fmt.Printf("droid-proxy updated %s -> %s\n", res.BeforeCommit, res.AfterCommit)
	} else if res.AfterCommit != "" {
		fmt.Printf("droid-proxy source already current at %s\n", res.AfterCommit)
	} else {
		fmt.Println("droid-proxy source already current")
	}
	if res.Built {
		fmt.Printf("rebuilt binary: %s\n", res.BinaryPath)
	}
}

func restartAfterUpdate(binary string, meta daemon.RuntimeMetadata, haveMeta bool) error {
	if daemon.LaunchdInstalled() {
		if err := daemon.RestartLaunchd(); err != nil {
			return err
		}
		fmt.Println("restarted launchd service.")
		return nil
	}

	wd, _ := os.Getwd()
	workDir := wd
	configPath := defaultConfigPath()
	envFile := ""
	if haveMeta {
		if meta.WorkDir != "" {
			workDir = meta.WorkDir
		}
		if meta.ConfigPath != "" {
			configPath = meta.ConfigPath
		}
		if meta.EnvFile != "" {
			envFile = meta.EnvFile
		}
	}
	if envFile == "" {
		envFile = daemon.ResolveEnvFile(workDir)
	}

	if err := daemon.StopWithTimeout(10 * time.Second); err != nil {
		return fmt.Errorf("stopping running proxy: %w", err)
	}
	args := []string{"start", "--config", configPath}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return fmt.Errorf("starting updated proxy: %s: %w", trimmed, err)
		}
		return fmt.Errorf("starting updated proxy: %w", err)
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		fmt.Println(trimmed)
	}
	return nil
}

// readBinaryHashQuietly reads the SHA-256 hash of a binary file, returning
// empty string on error. Used to capture the pre-upgrade binary hash for
// deferred provenance recording.
func readBinaryHashQuietly(path string) string {
	return migration.ReadBinaryHashForProvenance(path)
}
