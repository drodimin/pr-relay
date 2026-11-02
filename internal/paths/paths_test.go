package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// clearEnv blanks every variable StateDir and ConfigPath consult and points
// HOME at a temp dir, so tests never touch the real home directory.
func clearEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"PR_RELAY_STATE_DIR", "XDG_STATE_HOME", "PR_RELAY_CONFIG", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
	return home
}

func mode(t *testing.T, dir string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	return fi.Mode().Perm()
}

func TestStateDirOverride(t *testing.T) {
	clearEnv(t)
	want := filepath.Join(t.TempDir(), "x")
	t.Setenv("PR_RELAY_STATE_DIR", want)
	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if m := mode(t, got); m != 0o700 {
		t.Fatalf("mode = %o, want 700", m)
	}
}

func TestStateDirTightensExisting(t *testing.T) {
	clearEnv(t)
	want := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(want, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_RELAY_STATE_DIR", want)
	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if m := mode(t, got); m != 0o700 {
		t.Fatalf("mode = %o, want 700", m)
	}
}

func TestStateDirXDG(t *testing.T) {
	clearEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if want := filepath.Join(xdg, "pr-relay"); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if m := mode(t, got); m != 0o700 {
		t.Fatalf("mode = %o, want 700", m)
	}
}

func TestStateDirPlatformDefault(t *testing.T) {
	home := clearEnv(t)
	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	var want string
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Library", "Application Support", "pr-relay")
	} else {
		want = filepath.Join(home, ".local", "state", "pr-relay")
	}
	if got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if m := mode(t, got); m != 0o700 {
		t.Fatalf("mode = %o, want 700", m)
	}
}

func TestConfigPath(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want func(home string) string
	}{
		{
			name: "override",
			env:  map[string]string{"PR_RELAY_CONFIG": "/tmp/custom.toml", "XDG_CONFIG_HOME": "/tmp/xdg"},
			want: func(string) string { return "/tmp/custom.toml" },
		},
		{
			name: "xdg",
			env:  map[string]string{"XDG_CONFIG_HOME": "/tmp/xdg"},
			want: func(string) string { return "/tmp/xdg/pr-relay/config.toml" },
		},
		{
			name: "home default",
			want: func(home string) string { return filepath.Join(home, ".config", "pr-relay", "config.toml") },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := ConfigPath()
			if err != nil {
				t.Fatalf("ConfigPath: %v", err)
			}
			if want := tc.want(home); got != want {
				t.Fatalf("ConfigPath = %q, want %q", got, want)
			}
		})
	}
}

func TestConfigPathCreatesNothing(t *testing.T) {
	home := clearEnv(t)
	if _, err := ConfigPath(); err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Fatalf("ConfigPath created %s/.config (err=%v)", home, err)
	}
}
