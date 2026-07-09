package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	updater "github.com/trevoraspencer/droid-proxy/internal/update"
	"github.com/trevoraspencer/droid-proxy/internal/version"
)

type doctorResult struct {
	HardIssues []string
}

func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repoPath := fs.String("repo", "", "path to droid-proxy source checkout")
	_ = fs.Parse(args)

	res := writeDoctor(os.Stdout, *repoPath)
	if len(res.HardIssues) > 0 {
		os.Exit(1)
	}
}

func writeDoctor(out io.Writer, explicitRepo string) doctorResult {
	res := doctorResult{}
	exe, exeErr := currentExecutablePath()
	if exeErr != nil {
		res.HardIssues = append(res.HardIssues, exeErr.Error())
	}
	fmt.Fprintln(out, "droid-proxy doctor")
	if exeErr == nil {
		fmt.Fprintf(out, "executable: %s\n", exe)
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			fmt.Fprintf(out, "symlink target: %s\n", real)
		} else {
			fmt.Fprintf(out, "symlink target: %s\n", exe)
		}
	}
	info := version.Current()
	fmt.Fprintf(out, "version: %s\n", info.Version)
	fmt.Fprintf(out, "commit: %s\n", info.Commit)

	wd, _ := os.Getwd()
	repo := ""
	if exeErr == nil {
		if resolved, err := updater.ResolveRepoPath(explicitRepo, wd, exe); err == nil {
			repo = resolved
			fmt.Fprintf(out, "source repo: %s\n", repo)
			writeGitStatus(out, repo)
			if err := doctorUpdaterDryRun(repo, exe); err != nil {
				msg := "updater dry-run: issue: " + err.Error()
				fmt.Fprintln(out, msg)
				res.HardIssues = append(res.HardIssues, msg)
			} else {
				fmt.Fprintln(out, "updater dry-run: ok")
			}
		} else {
			if strings.TrimSpace(explicitRepo) != "" {
				msg := "source repo: issue: " + err.Error()
				fmt.Fprintln(out, msg)
				res.HardIssues = append(res.HardIssues, msg)
			} else {
				fmt.Fprintf(out, "source repo: not found (%v)\n", err)
				fmt.Fprintln(out, "updater dry-run: skipped (release install)")
			}
		}
	}

	if pid, running := daemon.IsRunning(); running {
		fmt.Fprintf(out, "daemon: running pid %d\n", pid)
	} else {
		fmt.Fprintln(out, "daemon: not running")
	}

	check, err := daemon.CheckService()
	if err != nil {
		msg := "service: issue: " + err.Error()
		fmt.Fprintln(out, msg)
		res.HardIssues = append(res.HardIssues, msg)
	} else if !check.Installed {
		if strings.TrimSpace(check.Path) != "" {
			fmt.Fprintf(out, "service: %s not installed (%s)\n", check.Kind, check.Path)
		} else {
			fmt.Fprintf(out, "service: not supported on %s\n", check.Kind)
		}
	} else {
		fmt.Fprintf(out, "service: %s installed (%s)\n", check.Kind, check.Path)
		if len(check.ProgramArguments) > 0 {
			fmt.Fprintf(out, "service ProgramArguments: %s\n", strings.Join(check.ProgramArguments, " "))
		}
		for _, issue := range check.Issues {
			msg := "service: issue: " + issue
			fmt.Fprintln(out, msg)
			res.HardIssues = append(res.HardIssues, msg)
		}
		for _, issue := range doctorServiceConfigIssues(check.ProgramArguments) {
			msg := "service: issue: " + issue
			fmt.Fprintln(out, msg)
			res.HardIssues = append(res.HardIssues, msg)
		}
	}

	if len(res.HardIssues) == 0 {
		fmt.Fprintln(out, "status: ok")
	} else {
		fmt.Fprintf(out, "status: %d issue(s)\n", len(res.HardIssues))
	}
	return res
}

func writeGitStatus(out io.Writer, repo string) {
	head := gitOutput(repo, "rev-parse", "--short=12", "HEAD")
	origin := gitOutput(repo, "rev-parse", "--short=12", "origin/main")
	if head != "" {
		fmt.Fprintf(out, "source HEAD: %s\n", head)
	}
	if origin == "" {
		fmt.Fprintln(out, "source origin/main: unavailable locally; run git fetch to refresh local refs")
		return
	}
	fmt.Fprintf(out, "source origin/main: %s\n", origin)
	if head == origin {
		fmt.Fprintln(out, "source freshness: HEAD matches local origin/main")
	} else {
		fmt.Fprintln(out, "source freshness: HEAD differs from local origin/main; run git fetch and droid-proxy update --dry-run")
	}
}

func gitOutput(repo string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func doctorUpdaterDryRun(repo, binary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := updater.Run(ctx, updater.Options{
		RepoPath:   repo,
		BinaryPath: binary,
		DryRun:     true,
	})
	return err
}
