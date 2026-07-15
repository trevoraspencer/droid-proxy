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

	"gopkg.in/yaml.v3"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/daemon"
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
			fmt.Printf("health: curl -s %s/health\n", listenURL(*configPath))
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

var (
	stopServiceInstalled = daemon.ServiceInstalled
	stopService          = daemon.StopService
	stopDaemon           = daemon.Stop
)

func runStop() {
	if err := stopProxy(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy stop error: %v\n", err)
		os.Exit(1)
	}
}

func stopProxy(out io.Writer) error {
	if stopServiceInstalled() {
		if err := stopService(); err != nil {
			return err
		}
		fmt.Fprintf(out, "stopped managed service %s.\n", daemon.ServiceDescription())
		fmt.Fprintln(out, "It starts again at next login; run 'droid-proxy service uninstall' to remove it, or 'droid-proxy restart' to start it now.")
		return nil
	}
	if err := stopDaemon(); err != nil {
		return err
	}
	fmt.Fprintln(out, "droid-proxy stopped.")
	return nil
}

func runStatus() {
	writeStatus(os.Stdout, daemon.IsRunning, daemon.ServiceRunning)
}

func writeStatus(out io.Writer, isRunning func() (int, bool), serviceState func() daemon.RuntimeState) {
	st := serviceState()
	if pid, running := isRunning(); running {
		fmt.Fprintf(out, "droid-proxy is running (pid %d)\n", pid)
		if st.Installed {
			fmt.Fprintf(out, "managed service: %s", st.Detail)
			if st.Running {
				fmt.Fprintf(out, " (pid %d)", st.PID)
			}
			fmt.Fprintln(out)
		}
		return
	}
	if st.Installed && st.Running {
		fmt.Fprintf(out, "droid-proxy is running under the managed service (pid %d); local pidfile state is stale\n", st.PID)
		return
	}
	fmt.Fprintln(out, "droid-proxy is not running.")
	if st.Installed {
		fmt.Fprintf(out, "service installed but not active (%s) — check 'droid-proxy logs'\n", st.Detail)
	}
}

func runRestart(args []string) {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	envFile := fs.String("env-file", "", "optional env file with API keys (export KEY=...)")
	noMigratePort := fs.Bool("no-migrate-port", false, "do not perform automatic port migration for this invocation")
	_ = fs.Parse(args)

	if *envFile == "" {
		*envFile = defaultEnvFileForConfig(*configPath)
	}
	if *noMigratePort {
		// Invocation-scoped opt-out: automatic migration is skipped for this
		// restart. The read-only omitted-port startup preflight remains
		// enforced. Explicit migrate-port is unaffected.
	}
	// Verified controlled restart: check for deferred upgrade provenance
	// and perform automatic migration if eligible. This is the only path
	// through which automatic migration runs.
	attemptManagedMigration(*configPath, *noMigratePort)
	if err := restartProxy(*configPath, *envFile); err != nil {
		fmt.Fprintf(os.Stderr, "droid-proxy restart error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("droid-proxy restarted.")
}

func restartProxy(configPath, envFile string) error {
	if daemon.ServiceInstalled() {
		return daemon.RestartService()
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
		printServiceUsage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "-h", "--help", "help":
		printServiceUsage(os.Stdout)
		return
	case "install":
		fs := flag.NewFlagSet("service install", flag.ExitOnError)
		configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
		_ = fs.Parse(args[1:])
		if err := validateServiceInstallConfig(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy service install error: %v\n", err)
			os.Exit(1)
		}
		// service install cannot infer upgrade provenance, but if deferred
		// provenance from a prior upgrade exists, consume it through the
		// controlled transaction before starting the service.
		attemptManagedMigration(*configPath, false)
		if err := daemon.InstallService(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy service install error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("droid-proxy installed as %s.\n", daemon.ServiceDescription())
		fmt.Println(" - Auto-starts on login")
		fmt.Println(" - Auto-restarts on crash")
		fmt.Printf(" - Logs: %s/stdout.log, %s/stderr.log\n", daemon.StateDir(), daemon.StateDir())
	case "uninstall":
		if err := daemon.UninstallService(); err != nil {
			fmt.Fprintf(os.Stderr, "droid-proxy service uninstall error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("droid-proxy removed from user services.")
	default:
		printServiceUsage(os.Stderr)
		os.Exit(2)
	}
}

func printServiceUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: droid-proxy service <install|uninstall> [--config config.yaml]")
}

func runLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	lines := fs.Int("n", 40, "number of log lines to show")
	_ = fs.Parse(args)

	path := filepath.Join(daemon.StateDir(), "stderr.log")
	if arg := fs.Arg(0); arg != "" {
		path = arg
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

// listenURL derives the proxy's listen URL from the config path for display.
// It tries a full config load (which applies defaults and IPv6 bracket
// serialization) and falls back to a minimal YAML parse.
func listenURL(configPath string) string {
	if cfg, err := config.Load(configPath); err == nil {
		return config.FormatListenURL(cfg.Listen.Host, cfg.Listen.Port)
	}
	var lf struct {
		Listen struct {
			Host string `yaml:"host"`
			Port int    `yaml:"port"`
		} `yaml:"listen"`
	}
	if data, err := os.ReadFile(configPath); err == nil {
		_ = yaml.Unmarshal(data, &lf)
	}
	host := lf.Listen.Host
	port := lf.Listen.Port
	if port == 0 {
		port = config.DefaultListenPort
	}
	return config.FormatListenURL(host, port)
}
