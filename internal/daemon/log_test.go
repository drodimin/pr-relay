package daemon

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return fi.Mode().Perm()
}

func TestLogging(t *testing.T) {
	t.Run("modes and message", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "log")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		l, err := OpenLog(dir)
		if err != nil {
			t.Fatalf("OpenLog: %v", err)
		}
		t.Cleanup(func() { _ = l.Close() })

		if got := mode(t, dir); got != 0o700 {
			t.Errorf("dir mode = %o, want 700", got)
		}
		want := filepath.Join(dir, logFileName)
		if l.Path() != want {
			t.Errorf("Path() = %q, want %q", l.Path(), want)
		}
		l.Logger().Info("hello from test", "pane", "w1:p3")
		if got := mode(t, want); got != 0o600 {
			t.Errorf("log mode = %o, want 600", got)
		}
		b, err := os.ReadFile(want)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "hello from test") {
			t.Errorf("log missing message: %q", b)
		}
		if strings.Contains(string(b), "GH_TOKEN") || strings.Contains(string(b), "ghp_") {
			t.Errorf("startup lines contain token text: %q", b)
		}
	})

	t.Run("existing loose file tightened", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, logFileName)
		if err := os.WriteFile(p, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		l, err := OpenLog(dir)
		if err != nil {
			t.Fatalf("OpenLog: %v", err)
		}
		t.Cleanup(func() { _ = l.Close() })
		l.Logger().Info("x")
		if got := mode(t, p); got != 0o600 {
			t.Errorf("log mode = %o, want 600", got)
		}
	})

	t.Run("rotation", func(t *testing.T) {
		old := maxSizeMB
		maxSizeMB = 1
		t.Cleanup(func() { maxSizeMB = old })

		dir := t.TempDir()
		l, err := OpenLog(dir)
		if err != nil {
			t.Fatalf("OpenLog: %v", err)
		}
		line := strings.Repeat("a", 4096)
		// About 6 MB of output: forces several rotations at 1 MB.
		for i := 0; i < 1500; i++ {
			l.Logger().Info(line)
			if i%300 == 299 {
				// Distinct backup timestamps (millisecond resolution).
				time.Sleep(5 * time.Millisecond)
			}
		}
		if err := l.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}

		// Old backups are pruned asynchronously; wait for the cap.
		var files []string
		deadline := time.Now().Add(3 * time.Second)
		for {
			files, err = filepath.Glob(filepath.Join(dir, "daemon*.log"))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) <= 1+maxBackups || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(files) < 2 {
			t.Fatalf("no rotation happened: %v", files)
		}
		if len(files) > 1+maxBackups {
			t.Fatalf("got %d log files, want at most %d: %v", len(files), 1+maxBackups, files)
		}
		for _, f := range files {
			if got := mode(t, f); got != 0o600 {
				t.Errorf("%s mode = %o, want 600", f, got)
			}
		}
	})

	t.Run("crash file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "c")
		f, err := SetCrash(dir)
		if err != nil {
			t.Fatalf("SetCrash: %v", err)
		}
		t.Cleanup(func() {
			_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
			_ = f.Close()
		})
		p := filepath.Join(dir, crashFileName)
		if f.Name() != p {
			t.Errorf("crash file = %q, want %q", f.Name(), p)
		}
		if got := mode(t, p); got != 0o600 {
			t.Errorf("crash mode = %o, want 600", got)
		}
		if got := mode(t, dir); got != 0o700 {
			t.Errorf("crash dir mode = %o, want 700", got)
		}
	})
}
