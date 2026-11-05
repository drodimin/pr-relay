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
//   - Control socket (D-01): PR_RELAY_SOCKET, then <PR_RELAY_STATE_DIR>/
//     pr-relay.sock when the state dir is overridden, then
//     $XDG_RUNTIME_DIR/pr-relay/pr-relay.sock, then <state dir>/pr-relay.sock.
//     A derived path of 104 bytes or more (the macOS sun_path limit) falls
//     back to os.TempDir()/pr-relay-<uid>/pr-relay.sock. The socket's parent
//     directory is created 0700 and refused if it is a symlink or owned by
//     another uid. The daemon singleton lock (pr-relay.sock.lock) and the
//     auto-start spawn lock (spawn.lock) sit next to the socket.
//   - Log dir (DMN-06): PR_RELAY_LOG_DIR, then ~/Library/Logs/pr-relay on
//     macOS, or $XDG_STATE_HOME/pr-relay/log (~/.local/state/pr-relay/log)
//     elsewhere. It is created 0700.
//   - The PR_RELAY_* overrides exist so tests never touch the real home dir.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// Environment overrides, consulted first.
const (
	EnvStateDir = "PR_RELAY_STATE_DIR"
	EnvConfig   = "PR_RELAY_CONFIG"
	EnvSocket   = "PR_RELAY_SOCKET"
	EnvLogDir   = "PR_RELAY_LOG_DIR"
)

const (
	appName    = "pr-relay"
	socketName = "pr-relay.sock"
	// maxSunPath is the macOS sun_path size (104 bytes including the NUL).
	// Linux allows 108; the stricter limit is applied everywhere.
	maxSunPath = 104
)

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

// SocketPath returns the control socket location and makes sure its parent
// directory exists with mode 0700 and is owned by the current user. An
// explicit PR_RELAY_SOCKET is used verbatim; only derived paths get the
// sun_path fallback.
func SocketPath() (string, error) {
	if p := os.Getenv(EnvSocket); p != "" {
		p = filepath.Clean(p)
		if err := ensurePrivateDir(filepath.Dir(p)); err != nil {
			return "", err
		}
		return p, nil
	}
	var p string
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" && os.Getenv(EnvStateDir) == "" {
		p = filepath.Join(filepath.Clean(xdg), appName, socketName)
	} else {
		dir, err := StateDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(dir, socketName)
	}
	if len(p) >= maxSunPath {
		p = filepath.Join(os.TempDir(), appName+"-"+strconv.Itoa(os.Getuid()), socketName)
	}
	if err := ensurePrivateDir(filepath.Dir(p)); err != nil {
		return "", err
	}
	return p, nil
}

// SocketLockPath returns the daemon singleton lock file, next to the socket.
func SocketLockPath() (string, error) {
	p, err := SocketPath()
	if err != nil {
		return "", err
	}
	return p + ".lock", nil
}

// SpawnLockPath returns the lock the CLI holds while auto-starting the
// daemon, in the socket's directory.
func SpawnLockPath() (string, error) {
	p, err := SocketPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "spawn.lock"), nil
}

// LogDir returns the daemon log directory, creating it with mode 0700.
func LogDir() (string, error) {
	dir := os.Getenv(EnvLogDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("paths: resolve home dir: %w", err)
		}
		switch {
		case runtime.GOOS == "darwin":
			dir = filepath.Join(home, "Library", "Logs", appName)
		case os.Getenv("XDG_STATE_HOME") != "":
			dir = filepath.Join(os.Getenv("XDG_STATE_HOME"), appName, "log")
		default:
			dir = filepath.Join(home, ".local", "state", appName, "log")
		}
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("paths: create log dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("paths: chmod log dir: %w", err)
	}
	return dir, nil
}

// ensurePrivateDir creates dir with mode 0700 and refuses it when it is a
// symlink, not a directory, or owned by another uid (T-04-10, T-04-11). This
// matters most for the shared os.TempDir() fallback.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("paths: create socket dir: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("paths: stat socket dir: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("paths: unsafe socket dir %s", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("paths: unsafe socket dir %s", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("paths: chmod socket dir: %w", err)
	}
	return nil
}
