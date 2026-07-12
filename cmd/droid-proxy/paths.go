package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
)

func defaultConfigPath() string {
	exe, _ := currentExecutablePath()
	meta, haveMeta := daemon.RuntimeMetadata{}, false
	if m, err := daemon.ReadRuntimeMetadata(); err == nil {
		meta = m
		haveMeta = true
	}
	return resolveDefaultConfigPath(".", exe, perUserConfigPath(), meta, haveMeta, regularFileExists)
}

func resolveDefaultConfigPath(currentDir, executable, userConfig string, meta daemon.RuntimeMetadata, haveMeta bool, exists func(string) bool) string {
	for _, name := range []string{"config.local.yaml", "config.yaml"} {
		candidate := filepath.Join(currentDir, name)
		if exists(candidate) {
			if currentDir == "." || currentDir == "" {
				return name
			}
			return candidate
		}
	}
	if haveMeta && meta.ConfigPath != "" && exists(meta.ConfigPath) {
		return meta.ConfigPath
	}
	if userConfig != "" && exists(userConfig) {
		return userConfig
	}
	if executable != "" {
		exeDir := filepath.Dir(executable)
		for _, name := range []string{"config.local.yaml", "config.yaml"} {
			candidate := filepath.Join(exeDir, name)
			if exists(candidate) {
				return candidate
			}
		}
	}
	return "config.yaml"
}

func perUserConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "droid-proxy", "config.yaml")
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func configWorkDir(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return "."
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			return wd
		}
		return "."
	}
	return filepath.Dir(absConfig)
}

func defaultEnvFileForConfig(configPath string) string {
	if envFile := daemon.RuntimeEnvFileForConfig(configPath); envFile != "" {
		return envFile
	}
	return daemon.ResolveEnvFile(configWorkDir(configPath))
}

func absPathOrOriginal(path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func runtimeMetadata(configPath, envFile, workDir string) (daemon.RuntimeMetadata, error) {
	exe, err := currentExecutablePath()
	if err != nil {
		return daemon.RuntimeMetadata{}, err
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return daemon.RuntimeMetadata{}, fmt.Errorf("config path: %w", err)
	}
	if envFile == "" {
		envFile = daemon.ResolveEnvFile(workDir)
	}
	if envFile != "" {
		if absEnv, err := filepath.Abs(envFile); err == nil {
			envFile = absEnv
		}
	}
	configModTime := ""
	if info, statErr := os.Stat(absConfig); statErr == nil {
		configModTime = info.ModTime().UTC().Format(time.RFC3339)
	}
	return daemon.RuntimeMetadata{
		PID:           os.Getpid(),
		Executable:    exe,
		ConfigPath:    absConfig,
		ConfigModTime: configModTime,
		EnvFile:       envFile,
		WorkDir:       workDir,
	}, nil
}

func currentExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable path: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// loadConfigEnv loads API keys from the managed secrets file and any repo
// env file so config.Load validation passes for commands that don't run the
// server.
func loadConfigEnv(configPath string) {
	workDir := "."
	if configPath != "" {
		if absConfig, err := filepath.Abs(configPath); err == nil {
			workDir = filepath.Dir(absConfig)
		}
	}
	_ = daemon.LoadLayeredEnv(workDir, daemon.RuntimeEnvFileForConfig(configPath))
}
