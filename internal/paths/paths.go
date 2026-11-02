// Package paths resolves pr-relay's per-user file locations: the state
// directory (state.json, its lock and quarantined files) and the optional
// config.toml.
//
// Resolved choices (phase 02 RESEARCH):
//   - State dir: PR_RELAY_STATE_DIR, then $XDG_STATE_HOME/pr-relay, then
//     ~/Library/Application Support/pr-relay on macOS or
//     ~/.local/state/pr-relay elsewhere. It is created 0700, and a
//     pre-existing looser directory is tightened to 0700 (ASVS V4).
//   - Config: PR_RELAY_CONFIG, then $XDG_CONFIG_HOME/pr-relay/config.toml,
//     then ~/.config/pr-relay/config.toml on both OSes (matching herdr's
//     ~/.config). Resolving the config path never creates anything.
//   - The PR_RELAY_* overrides exist so tests never touch the real home dir.
//
// Phase 4 adds the socket and log paths here.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Environment overrides, consulted first.
const (
	EnvStateDir = "PR_RELAY_STATE_DIR"
	EnvConfig   = "PR_RELAY_CONFIG"
)

const appName = "pr-relay"

// StateDir returns the per-user state directory, creating it with mode 0700
// (and tightening an existing directory to 0700).
func StateDir() (string, error) {
	dir := os.Getenv(EnvStateDir)
	if dir == "" {
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			dir = filepath.Join(xdg, appName)
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("paths: resolve home dir: %w", err)
			}
			if runtime.GOOS == "darwin" {
				dir = filepath.Join(home, "Library", "Application Support", appName)
			} else {
				dir = filepath.Join(home, ".local", "state", appName)
			}
		}
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("paths: create state dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("paths: chmod state dir: %w", err)
	}
	return dir, nil
}

// ConfigPath returns the location of the optional config.toml. The file and
// its directory may not exist; ConfigPath does not create them.
func ConfigPath() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return filepath.Clean(p), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appName, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", appName, "config.toml"), nil
}
