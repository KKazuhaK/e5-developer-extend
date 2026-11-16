// Package paths resolves the per-user configuration and data directories.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	appDir       = "e5renew"
	envConfigDir = "E5RENEW_CONFIG_DIR"
	envDataDir   = "E5RENEW_DATA_DIR"
	configFile   = "config.toml"
)

// ConfigDir returns the configuration directory without creating it.
func ConfigDir() (string, error) {
	home, _ := os.UserHomeDir()
	return resolve(runtime.GOOS, os.Getenv, home, true)
}

// DataDir returns the data directory without creating it.
func DataDir() (string, error) {
	home, _ := os.UserHomeDir()
	return resolve(runtime.GOOS, os.Getenv, home, false)
}

// ConfigFile returns the path of config.toml.
func ConfigFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFile), nil
}

// VaultDir returns the directory holding encrypted vault files.
func VaultDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vault"), nil
}

// EnsureDir creates dir (and parents) with mode 0700 and, except on Windows,
// tightens an existing dir to 0700.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}

func resolve(goos string, getenv func(string) string, home string, config bool) (string, error) {
	envName := envDataDir
	if config {
		envName = envConfigDir
	}
	if v := getenv(envName); v != "" {
		return filepath.Clean(v), nil
	}

	switch goos {
	case "windows":
		name := "LOCALAPPDATA"
		if config {
			name = "APPDATA"
		}
		base := getenv(name)
		if base == "" {
			return "", fmt.Errorf("%%%s%% is not set; set %s to choose a directory", name, envName)
		}
		return filepath.Join(base, appDir), nil
	case "darwin":
		if home == "" {
			return "", noHome(envName)
		}
		return filepath.Join(home, "Library", "Application Support", appDir), nil
	default:
		xdg, fallback := "XDG_DATA_HOME", filepath.Join(".local", "share")
		if config {
			xdg, fallback = "XDG_CONFIG_HOME", ".config"
		}
		// XDG requires absolute paths; ignore relative values. Check with the
		// Unix rule rather than the host's filepath.IsAbs, which differs on Windows.
		if base := getenv(xdg); strings.HasPrefix(base, "/") {
			return filepath.Join(base, appDir), nil
		}
		if home == "" {
			return "", noHome(envName)
		}
		return filepath.Join(home, fallback, appDir), nil
	}
}

func noHome(envName string) error {
	return errors.New("cannot determine the home directory; set " + envName + " to choose a directory")
}
