package herdr

import (
	"errors"
	"path/filepath"
)

// Environment variables herdr sets inside a pane.
const (
	EnvSocketPath = "HERDR_SOCKET_PATH"
	EnvSession    = "HERDR_SESSION"
	EnvPaneID     = "HERDR_PANE_ID"
)

var errNoHome = errors.New("herdr: cannot resolve socket: HOME unset")

// ResolveSocket returns the herdr socket for the calling process, reading
// the environment only through getenv (SUB-03). The order is
// HERDR_SOCKET_PATH, then HERDR_SESSION (~/.config/herdr/sessions/<name>/
// herdr.sock), then ~/.config/herdr/herdr.sock, with HOME from getenv. The
// result is absolute and cleaned. Symlinks are deliberately not resolved, so
// the same socket keeps the same key across calls (RESEARCH Pitfall 6); the
// path is only a dial target, and DialUnix still refuses a non-socket or
// another user's socket (T-04-05).
func ResolveSocket(getenv func(string) string) (string, error) {
	if p := getenv(EnvSocketPath); p != "" {
		return canonical(p)
	}
	home := getenv("HOME")
	if home == "" {
		return "", errNoHome
	}
	if s := getenv(EnvSession); s != "" {
		return canonical(filepath.Join(home, ".config", "herdr", "sessions", s, "herdr.sock"))
	}
	return canonical(filepath.Join(home, ".config", "herdr", "herdr.sock"))
}

func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// PaneFromEnv returns HERDR_PANE_ID, or "" outside a herdr pane.
func PaneFromEnv(getenv func(string) string) string { return getenv(EnvPaneID) }

// InHerdr reports whether the process runs inside a herdr pane.
func InHerdr(getenv func(string) string) bool { return PaneFromEnv(getenv) != "" }
