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

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	updater "github.com/trevoraspencer/droid-proxy/internal/update"
	"github.com/trevoraspencer/droid-proxy/internal/version"
)

type doctorResult struct {
	HardIssues []string
}

type doctorOptions struct {
	RepoPath        string
	ConfigPath      string
	ConfigExplicit  bool
	EnvFile         string
	EnvFileExplicit bool
}

var (
	doctorManagedEnvFile  = daemon.ManagedEnvFile
	doctorLoadLayeredEnv  = daemon.LoadLayeredEnv
	doctorServiceRunning  = daemon.ServiceRunning
	doctorRuntimeMetadata = daemon.ReadRuntimeMetadata
)

func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repoPath := fs.String("repo", "", "path to droid-proxy source checkout")
	configPath := fs.String("config", "", "path to config.yaml to validate")
	envFile := fs.String("env-file", "", "optional env file with API keys (export KEY=...)")
	_ = fs.Parse(args)

	opts := doctorOptions{
		RepoPath:   *repoPath,
		ConfigPath: *configPath,
		EnvFile:    *envFile,
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config":
			opts.ConfigExplicit = true
		case "env-file":
			opts.EnvFileExplicit = true
		}
	})
	if !opts.ConfigExplicit {
		opts.ConfigPath = defaultConfigPath()
	}

	res := writeDoctorWithOptions(os.Stdout, opts)
	if len(res.HardIssues) > 0 {
		os.Exit(1)
	}
}

func writeDoctor(out io.Writer, explicitRepo string) doctorResult {
	return writeDoctorWithOptions(out, doctorOptions{
		RepoPath:   explicitRepo,
		ConfigPath: defaultConfigPath(),
	})
}

func writeDoctorWithOptions(out io.Writer, opts doctorOptions) doctorResult {
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

	res.HardIssues = append(res.HardIssues, writeDoctorConfig(out, opts)...)

	wd, _ := os.Getwd()
	repo := ""
	if exeErr == nil {
		if resolved, err := updater.ResolveRepoPath(opts.RepoPath, wd, exe); err == nil {
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
			if strings.TrimSpace(opts.RepoPath) != "" {
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
		st := doctorServiceRunning()
		if st.Running {
			fmt.Fprintf(out, "service state: running (pid %d)\n", st.PID)
			if _, pidfileRunning := daemon.IsRunning(); !pidfileRunning {
				fmt.Fprintf(out, "daemon note: the managed service reports pid %d; pidfile state is stale\n", st.PID)
			}
		} else {
			// A deliberately stopped service is not a defect; report softly.
			fmt.Fprintf(out, "service state: not running (%s)\n", st.Detail)
		}
	}

	if len(res.HardIssues) == 0 {
		fmt.Fprintln(out, "status: ok")
	} else {
		fmt.Fprintf(out, "status: %d issue(s)\n", len(res.HardIssues))
	}
	return res
}

func writeDoctorConfig(out io.Writer, opts doctorOptions) []string {
	configPath := strings.TrimSpace(opts.ConfigPath)
	if configPath == "" {
		fmt.Fprintln(out, "config: not selected (user config dir unavailable)")
		fmt.Fprintln(out, "config load: skipped")
		return nil
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		msg := "config: issue: " + err.Error()
		fmt.Fprintln(out, msg)
		fmt.Fprintln(out, "config load: skipped")
		return []string{msg}
	}
	info, err := os.Stat(absConfig)
	if err != nil {
		if os.IsNotExist(err) && !opts.ConfigExplicit {
			fmt.Fprintf(out, "config: not found at %s\n", absConfig)
			fmt.Fprintln(out, "config load: skipped (run droid-proxy setup or pass --config)")
			return nil
		}
		msg := "config: issue: " + err.Error()
		if os.IsNotExist(err) {
			msg = "config: issue: not found at " + absConfig
		}
		fmt.Fprintln(out, msg)
		fmt.Fprintln(out, "config load: skipped")
		return []string{msg}
	}
	if info.IsDir() {
		msg := "config: issue: path is a directory: " + absConfig
		fmt.Fprintln(out, msg)
		fmt.Fprintln(out, "config load: skipped")
		return []string{msg}
	}

	fmt.Fprintf(out, "config: %s\n", absConfig)
	issues := writeDoctorEnvStatus(out, absConfig, opts)
	workDir := configWorkDir(absConfig)
	envFile := opts.EnvFile
	if !opts.EnvFileExplicit {
		envFile = defaultEnvFileForConfig(absConfig)
	}
	if err := doctorLoadLayeredEnv(workDir, envFile); err != nil {
		msg := "env load: issue: " + doctorSafeEnvError(err)
		fmt.Fprintln(out, msg)
		fmt.Fprintln(out, "config load: skipped (env file issue)")
		return append(issues, msg)
	}

	cfg, err := config.Load(absConfig)
	if err != nil {
		msg := "config load: issue: " + err.Error()
		fmt.Fprintln(out, msg)
		return append(issues, msg)
	}
	fmt.Fprintln(out, "config load: ok")
	writeDoctorConfigFreshness(out, absConfig)
	fmt.Fprintf(out, "listen: %s:%d\n", cfg.Listen.Host, cfg.Listen.Port)
	writeDoctorModels(out, cfg)
	return issues
}

// writeDoctorConfigFreshness soft-warns when the config file changed after the
// running proxy loaded it (per runtime.json). The running instance is healthy,
// just stale, so this is never a hard issue.
func writeDoctorConfigFreshness(out io.Writer, absConfig string) {
	meta, err := doctorRuntimeMetadata()
	if err != nil || !samePath(meta.ConfigPath, absConfig) {
		return
	}
	loadedAt, err := time.Parse(time.RFC3339, meta.ConfigModTime)
	if err != nil {
		// Older runtime.json files lack config_mtime; fall back to the
		// proxy start time recorded in updated_at.
		if loadedAt, err = time.Parse(time.RFC3339, meta.UpdatedAt); err != nil {
			return
		}
	}
	info, err := os.Stat(absConfig)
	if err != nil {
		return
	}
	if info.ModTime().Truncate(time.Second).After(loadedAt.Truncate(time.Second)) {
		fmt.Fprintln(out, "config: changed since the proxy started — restart droid-proxy to apply")
	}
}

func doctorSafeEnvError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if idx := strings.Index(msg, "invalid env line "); idx >= 0 {
		return msg[:idx] + "invalid env line"
	}
	return msg
}

func writeDoctorEnvStatus(out io.Writer, configPath string, opts doctorOptions) []string {
	var issues []string
	managed := doctorManagedEnvFile()
	issues = append(issues, writeDoctorEnvPath(out, "env managed", managed, false)...)
	override := strings.TrimSpace(opts.EnvFile)
	required := opts.EnvFileExplicit
	if !opts.EnvFileExplicit {
		override = defaultEnvFileForConfig(configPath)
	}
	if samePath(override, managed) {
		return issues
	}
	if override == "" {
		fmt.Fprintln(out, "env override: none")
		return issues
	}
	label := "env override"
	if opts.EnvFileExplicit {
		label = "env override (--env-file)"
	}
	return append(issues, writeDoctorEnvPath(out, label, override, required)...)
}

func writeDoctorEnvPath(out io.Writer, label, path string, required bool) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		fmt.Fprintf(out, "%s: none\n", label)
		return nil
	}
	display := path
	if abs, err := filepath.Abs(path); err == nil {
		display = abs
	}
	info, err := os.Stat(display)
	if err != nil {
		if os.IsNotExist(err) {
			if required {
				msg := fmt.Sprintf("%s: issue: not found at %s", label, display)
				fmt.Fprintln(out, msg)
				return []string{msg}
			}
			fmt.Fprintf(out, "%s: not found at %s\n", label, display)
			return nil
		}
		msg := fmt.Sprintf("%s: issue: %v", label, err)
		fmt.Fprintln(out, msg)
		return []string{msg}
	}
	if info.IsDir() {
		msg := fmt.Sprintf("%s: issue: path is a directory: %s", label, display)
		fmt.Fprintln(out, msg)
		return []string{msg}
	}
	fmt.Fprintf(out, "%s: found %s\n", label, display)
	return nil
}

func samePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return absA == absB
	}
	return a == b
}

func writeDoctorModels(out io.Writer, cfg *config.Config) {
	if cfg == nil {
		return
	}
	agentReady := 0
	for _, model := range cfg.Models {
		if model.AgentReady() {
			agentReady++
		}
	}
	fmt.Fprintf(out, "models: %d configured, %d agent-ready\n", len(cfg.Models), agentReady)
	for _, model := range cfg.Models {
		fmt.Fprintf(out, "model %q: factory=%s upstream=%s auth=%s agent_ready=%t\n",
			model.Alias,
			model.FactoryProvider,
			model.UpstreamProtocol,
			doctorModelAuthSource(model),
			model.AgentReady(),
		)
	}
}

func doctorModelAuthSource(model *config.Model) string {
	if model == nil {
		return "unknown"
	}
	if model.OAuthProvider != "" {
		if strings.TrimSpace(model.OAuthAccount) != "" {
			return "oauth:" + string(model.OAuthProvider) + " account=" + model.OAuthAccount
		}
		return "oauth:" + string(model.OAuthProvider)
	}
	if strings.TrimSpace(model.KnownAuth) != "" {
		return "known_auth:" + model.KnownAuth
	}
	if strings.TrimSpace(model.APIKeyEnv) != "" {
		return "api_key_env:" + model.APIKeyEnv
	}
	return "none"
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
