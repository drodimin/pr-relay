package scratchguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const refusalText = "only scratch herdr sessions (a path containing /sessions/) are allowed"

func wantRefused(t *testing.T, path string) {
	t.Helper()
	err := Check(path)
	if err == nil || !strings.Contains(err.Error(), refusalText) || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("Check(%q) = %v, want a refusal", path, err)
	}
}

func wantAccepted(t *testing.T, path string) {
	t.Helper()
	if err := Check(path); err != nil {
		t.Errorf("Check(%q) = %v, want nil", path, err)
	}
}

func TestCheckLexical(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wantRefused(t, "")
	wantRefused(t, filepath.Join(home, ".config/herdr/herdr.sock"))
	wantRefused(t, filepath.Join(home, ".config/herdr/./herdr.sock"))
	wantRefused(t, "/tmp/x/herdr.sock")
	wantRefused(t, "/tmp/sessionsx/h.sock")
	// The parent does not exist, so the lexical result stands.
	wantAccepted(t, filepath.Join(home, ".config/herdr/sessions/prspike-does-not-exist-xyz/herdr.sock"))
}

func TestCheckRealSessionsDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "sessions", "t")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// On macOS TempDir lives under /var -> /private/var; the resolved path
	// still contains /sessions/.
	wantAccepted(t, filepath.Join(dir, "h.sock"))
}

func TestCheckSymlinkBypass(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "sessions")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// Lexically <tmp>/sessions/h.sock; resolved <tmp>/real/h.sock.
	wantRefused(t, filepath.Join(link, "h.sock"))

	// A deeper path through the same symlink resolves outside /sessions/ too.
	if err := os.MkdirAll(filepath.Join(real, "t"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, filepath.Join(link, "t", "h.sock"))
}

// TestCheckSymlinkToDefaultHerdrDir points a /sessions/ symlink at a
// directory standing in for ~/.config/herdr by overriding HOME.
func TestCheckSymlinkToDefaultHerdrDir(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	herdrDir := filepath.Join(home, ".config", "herdr", "sessions", "x")
	if err := os.MkdirAll(herdrDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	// <tmp>/s/sessions/p -> ~/.config/herdr: the resolved socket is the
	// default ~/.config/herdr/herdr.sock.
	base := filepath.Join(tmp, "s", "sessions")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "p")
	if err := os.Symlink(filepath.Join(home, ".config", "herdr"), link); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, filepath.Join(link, "herdr.sock"))
	// A real session directory under the same home is still accepted.
	wantAccepted(t, filepath.Join(herdrDir, "herdr.sock"))
}

func TestCheckStatErrorRefuses(t *testing.T) {
	tmp := t.TempDir()
	// A file where a directory is expected gives ENOTDIR, not not-exist.
	f := filepath.Join(tmp, "sessions")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, filepath.Join(f, "x", "h.sock"))
}
