// Package scratchguard refuses herdr sockets outside scratch sessions (T-03-30).
//
// Developer tooling that types prompts into herdr panes (dev deliver and the
// opt-in live test) must never reach the user's real session. Only sockets
// under a /sessions/ directory are allowed, and the default
// ~/.config/herdr/herdr.sock is always refused. Symlinks in the parent
// directory are resolved and the resolved path is checked again, so a
// /sessions/ symlink to the real herdr directory is refused (IN-05).
package scratchguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Check returns nil only when path names a scratch herdr session socket. It
// never dials.
func Check(path string) error {
	refuse := fmt.Errorf("refusing %s: only scratch herdr sessions (a path containing /sessions/) are allowed", path)
	if path == "" {
		return refuse
	}
	home, _ := os.UserHomeDir()
	defaults := defaultSockets(home)

	clean := filepath.Clean(path)
	if !allowed(clean, defaults) {
		return refuse
	}

	dir, err := filepath.EvalSymlinks(filepath.Dir(clean))
	switch {
	case err == nil:
		if !allowed(filepath.Join(dir, filepath.Base(clean)), defaults) {
			return refuse
		}
	case errors.Is(err, fs.ErrNotExist):
		// Nothing to resolve yet; the lexical result stands.
	default:
		return refuse
	}
	return nil
}

// defaultSockets lists the default herdr socket lexically and, when the
// herdr config directory exists, with symlinks resolved.
func defaultSockets(home string) []string {
	if home == "" {
		return nil
	}
	dir := filepath.Join(home, ".config", "herdr")
	out := []string{filepath.Join(dir, "herdr.sock")}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		out = append(out, filepath.Join(r, "herdr.sock"))
	}
	return out
}

func allowed(p string, defaults []string) bool {
	for _, d := range defaults {
		if p == d {
			return false
		}
	}
	return strings.Contains(p, "/sessions/")
}
