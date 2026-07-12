package daemon

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const systemdUnitName = "droid-proxy.service"

var systemctlRunner = runSystemctl

type systemdUnitData struct {
	Executable string
	ConfigPath string
	EnvFile    string
	WorkDir    string
	LogDir     string
}

type SystemdUnitCheck struct {
	Path             string
	Installed        bool
	ProgramArguments []string
	Issues           []string
}

func SystemdUnitPath() string {
	configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configHome == "" {
		configHome = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(configHome, "systemd", "user", systemdUnitName)
}

func SystemdUserInstalled() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat(SystemdUnitPath())
	return err == nil
}

func InstallSystemdUser(configPath string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("service install is supported on Linux only (systemd user)")
	}
	absConfig, workDir, err := validateServiceConfig(configPath)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	path := SystemdUnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data := systemdUnitData{
		Executable: exe,
		ConfigPath: absConfig,
		EnvFile:    ResolveExistingEnvFile(workDir),
		WorkDir:    workDir,
		LogDir:     stateDir(),
	}
	if err := writeSystemdUnit(path, data); err != nil {
		return err
	}
	if err := systemctlRunner("daemon-reload"); err != nil {
		return err
	}
	return systemctlRunner("enable", "--now", systemdUnitName)
}

func RestartSystemdUser() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd user restart is supported on Linux only")
	}
	if !SystemdUserInstalled() {
		return fmt.Errorf("systemd user service not installed")
	}
	return systemctlRunner("restart", systemdUnitName)
}

func UninstallSystemdUser() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("service uninstall is supported on Linux only (systemd user)")
	}
	path := SystemdUnitPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("systemd user service not installed")
	}
	_ = systemctlRunner("disable", "--now", systemdUnitName)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing systemd unit: %w", err)
	}
	return systemctlRunner("daemon-reload")
}

func CheckSystemdUnit(path string) (SystemdUnitCheck, error) {
	check := SystemdUnitCheck{Path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return check, nil
		}
		return check, err
	}
	check.Installed = true
	args, err := systemdExecStartArguments(raw)
	if err != nil {
		check.Issues = append(check.Issues, "could not parse ExecStart: "+err.Error())
		return check, nil
	}
	check.ProgramArguments = args
	check.Issues = append(check.Issues, serviceArgumentIssues(args)...)
	return check, nil
}

func writeSystemdUnit(path string, data systemdUnitData) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating systemd unit: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting systemd unit permissions: %w", err)
	}
	if _, err := io.WriteString(tmp, systemdUnitContents(data)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing systemd unit: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing systemd unit: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("setting systemd unit permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("installing systemd unit: %w", err)
	}
	return nil
}

func systemdUnitContents(data systemdUnitData) string {
	args := []string{data.Executable, "start", "--foreground", "--config", data.ConfigPath}
	if strings.TrimSpace(data.EnvFile) != "" {
		args = append(args, "--env-file", data.EnvFile)
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Droid Proxy\n")
	b.WriteString("After=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + systemdJoinArgs(args) + "\n")
	b.WriteString("WorkingDirectory=" + systemdUnitValue(data.WorkDir) + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2\n")
	b.WriteString("StandardOutput=append:" + systemdUnitValue(filepath.Join(data.LogDir, "stdout.log")) + "\n")
	b.WriteString("StandardError=append:" + systemdUnitValue(filepath.Join(data.LogDir, "stderr.log")) + "\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

func systemdJoinArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, systemdQuoteArg(arg))
	}
	return strings.Join(quoted, " ")
}

func systemdQuoteArg(arg string) string {
	escaped := systemdUnitValue(arg)
	if escaped == "" {
		return `""`
	}
	if strings.ContainsAny(escaped, " \t\n\"\\") {
		escaped = strings.ReplaceAll(escaped, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		return `"` + escaped + `"`
	}
	return escaped
}

func systemdUnitValue(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func systemdExecStartArguments(raw []byte) ([]string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "ExecStart=") {
			return parseSystemdCommandLine(strings.TrimPrefix(line, "ExecStart="))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("missing ExecStart")
}

func parseSystemdCommandLine(line string) ([]string, error) {
	var args []string
	var b strings.Builder
	inQuote := false
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			args = append(args, strings.ReplaceAll(b.String(), "%%", "%"))
			b.Reset()
		}
	}
	for _, r := range line {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t'):
			flush()
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	return args, nil
}

func runSystemctl(args ...string) error {
	cmdArgs := append([]string{"--user"}, args...)
	out, err := exec.Command("systemctl", cmdArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %s: %w", strings.Join(cmdArgs, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

func systemdUnitMode(path string) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}
