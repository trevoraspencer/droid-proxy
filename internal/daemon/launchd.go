package daemon

import (
	"encoding/xml"
	"fmt"
	"io"
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
{{- if .EnvFile }}
        <string>--env-file</string>
        <string>{{.EnvFile}}</string>
{{- end }}
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

var launchAgentLoader = loadLaunchAgent

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

func LaunchdPlistPath() string {
	return plistPath()
}

func LaunchdInstalled() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := os.Stat(plistPath())
	return err == nil
}

type LaunchdPlistCheck struct {
	Path             string
	Installed        bool
	ProgramArguments []string
	Issues           []string
}

func CheckLaunchdPlist(path string) (LaunchdPlistCheck, error) {
	check := LaunchdPlistCheck{Path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return check, nil
		}
		return check, err
	}
	check.Installed = true
	args, err := plistProgramArguments(raw)
	if err != nil {
		check.Issues = append(check.Issues, "could not parse ProgramArguments: "+err.Error())
		return check, nil
	}
	check.ProgramArguments = args
	if len(args) == 0 {
		check.Issues = append(check.Issues, "missing ProgramArguments")
		return check, nil
	}
	check.Issues = append(check.Issues, serviceArgumentIssues(args)...)
	return check, nil
}

func plistProgramArguments(raw []byte) ([]string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	inProgramArguments := false
	waitingForProgramArray := false
	var args []string
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return args, nil
			}
			return nil, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			switch tok.Name.Local {
			case "key":
				var key string
				if err := dec.DecodeElement(&key, &tok); err != nil {
					return nil, err
				}
				waitingForProgramArray = key == "ProgramArguments"
			case "array":
				if waitingForProgramArray {
					inProgramArguments = true
					waitingForProgramArray = false
				}
			case "string":
				if inProgramArguments {
					var value string
					if err := dec.DecodeElement(&value, &tok); err != nil {
						return nil, err
					}
					args = append(args, value)
				}
			}
		case xml.EndElement:
			if tok.Name.Local == "array" && inProgramArguments {
				return args, nil
			}
		}
	}
}

// InstallLaunchd registers droid-proxy as a user launchd agent.
func InstallLaunchd(configPath string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("service install is supported on macOS only (launchd)")
	}

	absConfig, workDir, err := ValidateLaunchdConfig(configPath)
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
	_ = os.Remove(filepath.Join(stateDir(), "run.sh"))

	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data := plistData{
		Label:      launchdLabel,
		Executable: exe,
		ConfigPath: absConfig,
		EnvFile:    ResolveExistingEnvFile(workDir),
		WorkDir:    workDir,
		LogDir:     stateDir(),
	}
	if err := writeLaunchdPlist(path, data); err != nil {
		return err
	}

	return launchAgentLoader(path)
}

func ValidateLaunchdConfig(configPath string) (string, string, error) {
	return validateServiceConfig(configPath)
}

func ResolveExistingEnvFile(workDir string) string {
	envFile := ResolveEnvFile(workDir)
	if strings.TrimSpace(envFile) == "" {
		return ""
	}
	absEnv, err := filepath.Abs(envFile)
	if err != nil {
		return ""
	}
	info, err := os.Stat(absEnv)
	if err != nil || info.IsDir() {
		return ""
	}
	return absEnv
}

func writeLaunchdPlist(path string, data plistData) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating plist: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting plist permissions: %w", err)
	}
	if err := plistTemplate.Execute(tmp, data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing plist: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing plist: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("setting plist permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("installing plist: %w", err)
	}
	return nil
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

var launchctlKickstart = func(target string) (string, error) {
	out, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput()
	return string(out), err
}

func RestartLaunchd() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchd restart is supported on macOS only")
	}
	if !LaunchdInstalled() {
		return fmt.Errorf("launchd service not installed")
	}
	return restartLaunchdService()
}

func restartLaunchdService() error {
	uid := os.Getuid()
	target := "gui/" + strconv.Itoa(uid) + "/" + launchdLabel
	out, err := launchctlKickstart(target)
	if err == nil {
		return nil
	}
	// After `droid-proxy stop` (bootout) the agent is installed but not
	// loaded; kickstart cannot start it, so bootstrap the plist instead.
	if strings.Contains(out, "Could not find service") {
		return launchAgentLoader(plistPath())
	}
	return fmt.Errorf("launchctl kickstart: %s: %w", strings.TrimSpace(out), err)
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
