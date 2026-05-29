package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

const launchdLabel = "com.droid-proxy.agent"

var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.Executable}}</string>
        <string>start</string>
        <string>--foreground</string>
        <string>--config</string>
        <string>{{.ConfigPath}}</string>
        <string>--env-file</string>
        <string>{{.EnvFile}}</string>
    </array>
    <key>WorkingDirectory</key>
    <string>{{.WorkDir}}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.LogDir}}/stdout.log</string>
    <key>StandardErrorPath</key>
    <string>{{.LogDir}}/stderr.log</string>
</dict>
</plist>
`))

type plistData struct {
	Label      string
	Executable string
	ConfigPath string
	EnvFile    string
	WorkDir    string
	LogDir     string
}

func plistPath() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", launchdLabel+".plist")
}

// InstallLaunchd registers droid-proxy as a user launchd agent.
func InstallLaunchd(configPath string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("service install is supported on macOS only (launchd)")
	}

	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("config path: %w", err)
	}
	workDir := filepath.Dir(absConfig)

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(stateDir, "run.sh"))

	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating plist: %w", err)
	}
	defer f.Close()

	data := plistData{
		Label:      launchdLabel,
		Executable: exe,
		ConfigPath: absConfig,
		EnvFile:    ResolveEnvFile(workDir),
		WorkDir:    workDir,
		LogDir:     stateDir,
	}
	if err := plistTemplate.Execute(f, data); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}

	return loadLaunchAgent(path)
}

func loadLaunchAgent(path string) error {
	uid := os.Getuid()
	domain := "gui/" + strconv.Itoa(uid)
	out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), "already") || strings.Contains(string(out), "Existing") {
		_ = exec.Command("launchctl", "bootout", domain, path).Run()
		out, err = exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput()
		if err == nil {
			return nil
		}
	}
	out, err = exec.Command("launchctl", "load", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap/load: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// UninstallLaunchd removes the launchd agent.
func UninstallLaunchd() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("service uninstall is supported on macOS only (launchd)")
	}

	path := plistPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("launchd service not installed")
	}

	uid := os.Getuid()
	domain := "gui/" + strconv.Itoa(uid)
	if out, err := exec.Command("launchctl", "bootout", domain, path).CombinedOutput(); err != nil {
		_ = out
		if out, err := exec.Command("launchctl", "unload", path).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootout/unload: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing plist: %w", err)
	}
	return nil
}
