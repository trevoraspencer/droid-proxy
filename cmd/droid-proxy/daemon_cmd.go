package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"droid-proxy/internal/daemon"
)

func runStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	envFile := fs.String("env-file", "", "optional env file with API keys (export KEY=...)")
	foreground := fs.Bool("foreground", false, "run in foreground (internal)")
	_ = fs.Parse(args)

	if *envFile == "" {
		*envFile = defaultEnvFileForConfig(*configPath)
	}

	if *foreground {
		if err := runServer(*configPath, *envFile, true); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	daemon.CleanStalePID()
	if pid, running := daemon.IsRunning(); running {
		fmt.Fprintf(os.Stderr, "droid-proxy already running (pid %d)\n", pid)
		os.Exit(1)
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy start error: %v\n", err)
		os.Exit(1)
	}

	configArg := absPathOrOriginal(*configPath)
	envArg := absPathOrOriginal(*envFile)
	child := exec.Command(exe, "start", "--foreground", "--config", configArg, "--env-file", envArg)
	child.Env = os.Environ()
	child.Dir = configWorkDir(configArg)
	child.Stdout = nil
	child.Stderr = nil
	child.Stdin = nil
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy start error: %v\n", err)
		os.Exit(1)
	}

	// Wait briefly for child to write PID or exit.
	for i := 0; i < 30; i++ {
		if pid, running := daemon.IsRunning(); running {
			fmt.Printf("droid-proxy started (pid %d)\n", pid)
			fmt.Printf("health: curl -s http://127.0.0.1:8787/health\n")
			return
		}
		if err := child.Process.Signal(syscall.Signal(0)); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy exited during startup\n")
			fmt.Fprintf(os.Stderr, "logs: droid-proxy logs\n")
			os.Exit(1)
		}
		//nolint:revive // short startup poll
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "droid-proxy start timed out waiting for PID file\n")
	os.Exit(1)
}

func runStop() {
	if err := daemon.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy stop error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("droid-proxy stopped.")
}

func runStatus() {
	if pid, running := daemon.IsRunning(); running {
		fmt.Printf("droid-proxy is running (pid %d)\n", pid)
		return
	}
	fmt.Println("droid-proxy is not running.")
}

func runRestart(args []string) {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	envFile := fs.String("env-file", "", "optional env file with API keys (export KEY=...)")
	_ = fs.Parse(args)

	if *envFile == "" {
		*envFile = defaultEnvFileForConfig(*configPath)
	}
	if err := restartProxy(*configPath, *envFile); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy restart error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("droid-proxy restarted.")
}

func restartProxy(configPath, envFile string) error {
	if daemon.LaunchdInstalled() {
		return daemon.RestartLaunchd()
	}
	if _, running := daemon.IsRunning(); running {
		if err := daemon.StopWithTimeout(10 * time.Second); err != nil {
			return fmt.Errorf("stopping running proxy: %w", err)
		}
	}
	daemon.CleanStalePID()
	exe, err := currentExecutablePath()
	if err != nil {
		return err
	}
	configArg := absPathOrOriginal(configPath)
	envArg := absPathOrOriginal(envFile)
	args := []string{"start", "--config", configArg}
	if envFile != "" {
		args = append(args, "--env-file", envArg)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = configWorkDir(configArg)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return fmt.Errorf("starting proxy: %s: %w", trimmed, err)
		}
		return fmt.Errorf("starting proxy: %w", err)
	}
	return nil
}

func runService(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: droid-proxy service <install|uninstall> [--config config.yaml]")
		os.Exit(2)
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ExitOnError)
		configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
		_ = fs.Parse(args[1:])
		if err := daemon.InstallLaunchd(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy service install error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("droid-proxy installed as launchd service.")
		fmt.Println(" - Auto-starts on login")
		fmt.Println(" - Auto-restarts on crash")
		fmt.Printf(" - Process name shown to macOS: droid-proxy\n")
		fmt.Printf(" - Logs: %s/stdout.log, %s/stderr.log\n", daemon.StateDir(), daemon.StateDir())
	case "uninstall":
		if err := daemon.UninstallLaunchd(); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy service uninstall error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("droid-proxy removed from launchd services.")
	default:
		fmt.Fprintln(os.Stderr, "usage: droid-proxy service <install|uninstall> [--config config.yaml]")
		os.Exit(2)
	}
}

func runLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	lines := fs.Int("n", 40, "number of log lines to show")
	_ = fs.Parse(args)

	path := filepath.Join(daemon.StateDir(), "stderr.log")
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		path = args[0]
	}

	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy logs error: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: install the service first, or check %s\n", daemon.StateDir())
		os.Exit(1)
	}
	defer f.Close()

	out, err := tailLines(f, *lines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy logs error: %v\n", err)
		os.Exit(1)
	}
	if len(out) == 0 {
		fmt.Println("(no log output yet)")
		return
	}
	fmt.Println(strings.Join(out, "\n"))
}

func tailLines(r io.Reader, n int) ([]string, error) {
	reader := bufio.NewReader(r)
	var lines []string
	var pendingBlank []string
	seenContent := false
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if strings.TrimSpace(line) == "" {
				if seenContent {
					pendingBlank = append(pendingBlank, line)
				}
			} else {
				if seenContent {
					for _, blank := range pendingBlank {
						lines = appendTailLine(lines, blank, n)
					}
				}
				pendingBlank = pendingBlank[:0]
				lines = appendTailLine(lines, line, n)
				seenContent = true
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return lines, nil
}

func appendTailLine(lines []string, line string, n int) []string {
	if n > 0 && len(lines) == n {
		copy(lines, lines[1:])
		lines[n-1] = line
		return lines
	}
	return append(lines, line)
}
