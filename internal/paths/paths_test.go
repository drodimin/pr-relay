package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// clearEnv blanks every variable StateDir and ConfigPath consult and points
// HOME at a temp dir, so tests never touch the real home directory.
func clearEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"PR_RELAY_STATE_DIR", "XDG_STATE_HOME", "PR_RELAY_CONFIG", "XDG_CONFIG_HOME", "PR_RELAY_SOCKET", "PR_RELAY_LOG_DIR", "XDG_RUNTIME_DIR"} {
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

// shortTemp returns a short temp dir (os.MkdirTemp under the system temp)
// so socket paths stay under the sun_path limit.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "prr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestSocketPathOverride(t *testing.T) {
	clearEnv(t)
	want := filepath.Join(shortTemp(t), "x", "y.sock")
	t.Setenv("PR_RELAY_SOCKET", want)
	t.Setenv("XDG_RUNTIME_DIR", shortTemp(t))
	got, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
	if m := mode(t, filepath.Dir(got)); m != 0o700 {
		t.Fatalf("socket dir mode = %o, want 700", m)
	}
}

func TestSocketPathStateDirBeatsRuntimeDir(t *testing.T) {
	clearEnv(t)
	d := shortTemp(t)
	t.Setenv("PR_RELAY_STATE_DIR", d)
	t.Setenv("XDG_RUNTIME_DIR", shortTemp(t))
	got, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if want := filepath.Join(d, "pr-relay.sock"); got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
}

func TestSocketPathRuntimeDir(t *testing.T) {
	clearEnv(t)
	r := shortTemp(t)
	t.Setenv("XDG_RUNTIME_DIR", r)
	got, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if want := filepath.Join(r, "pr-relay", "pr-relay.sock"); got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
	if m := mode(t, filepath.Dir(got)); m != 0o700 {
		t.Fatalf("socket dir mode = %o, want 700", m)
	}
}

func TestSocketPathDefaultsToStateDir(t *testing.T) {
	clearEnv(t)
	t.Setenv("HOME", shortTemp(t))
	sd, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", shortTemp(t))
	want := filepath.Join(sd, "pr-relay.sock")
	if len(want) >= 104 {
		// macOS temp dirs are long; the default then takes the fallback.
		want = filepath.Join(os.TempDir(), "pr-relay-"+strconv.Itoa(os.Getuid()), "pr-relay.sock")
	}
	got, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
}

func TestSocketPathLongFallsBack(t *testing.T) {
	clearEnv(t)
	t.Setenv("TMPDIR", shortTemp(t))
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 60))
	t.Setenv("PR_RELAY_STATE_DIR", long)
	got, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	wantDir := filepath.Join(os.TempDir(), "pr-relay-"+strconv.Itoa(os.Getuid()))
	if want := filepath.Join(wantDir, "pr-relay.sock"); got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
	if len(got) >= 104 {
		t.Fatalf("fallback %q is still too long", got)
	}
	if m := mode(t, wantDir); m != 0o700 {
		t.Fatalf("fallback dir mode = %o, want 700", m)
	}
}

func TestSocketPathRefusesSymlinkFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("TMPDIR", shortTemp(t))
	link := filepath.Join(os.TempDir(), "pr-relay-"+strconv.Itoa(os.Getuid()))
	if err := os.Symlink(shortTemp(t), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_RELAY_STATE_DIR", filepath.Join(t.TempDir(), strings.Repeat("f", 110)))
	_, err := SocketPath()
	if err == nil || !strings.Contains(err.Error(), "unsafe socket dir") {
		t.Fatalf("SocketPath err = %v, want unsafe socket dir", err)
	}
}

func TestLockPaths(t *testing.T) {
	clearEnv(t)
	t.Setenv("PR_RELAY_STATE_DIR", shortTemp(t))
	sock, err := SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := SocketLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if lock != sock+".lock" {
		t.Fatalf("SocketLockPath = %q, want %q", lock, sock+".lock")
	}
	spawn, err := SpawnLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(sock), "spawn.lock"); spawn != want {
		t.Fatalf("SpawnLockPath = %q, want %q", spawn, want)
	}
}

func TestLogDir(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		clearEnv(t)
		want := filepath.Join(t.TempDir(), "logs")
		t.Setenv("PR_RELAY_LOG_DIR", want)
		got, err := LogDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("LogDir = %q, want %q", got, want)
		}
		if m := mode(t, got); m != 0o700 {
			t.Fatalf("mode = %o, want 700", m)
		}
	})
	t.Run("xdg", func(t *testing.T) {
		home := clearEnv(t)
		xdg := t.TempDir()
		t.Setenv("XDG_STATE_HOME", xdg)
		got, err := LogDir()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(xdg, "pr-relay", "log")
		if runtime.GOOS == "darwin" {
			want = filepath.Join(home, "Library", "Logs", "pr-relay")
		}
		if got != want {
			t.Fatalf("LogDir = %q, want %q", got, want)
		}
		if m := mode(t, got); m != 0o700 {
			t.Fatalf("mode = %o, want 700", m)
		}
	})
	t.Run("home default", func(t *testing.T) {
		home := clearEnv(t)
		got, err := LogDir()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, ".local", "state", "pr-relay", "log")
		if runtime.GOOS == "darwin" {
			want = filepath.Join(home, "Library", "Logs", "pr-relay")
		}
		if got != want {
			t.Fatalf("LogDir = %q, want %q", got, want)
		}
	})
}
