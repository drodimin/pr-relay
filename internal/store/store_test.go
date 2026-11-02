package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustOpen(t *testing.T, dir string) (*Store, *State) {
	t.Helper()
	s, st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, st
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, NewState()) {
		t.Fatalf("Open(empty) = %#v, want NewState", st)
	}
	if s.Dir() != dir {
		t.Fatalf("Dir = %q, want %q", s.Dir(), dir)
	}
	want := fullState()
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, got := mustOpen(t, dir)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded state differs:\n got %#v\nwant %#v", got, want)
	}
}

func TestStoreSaveIndented(t *testing.T) {
	dir := t.TempDir()
	s, _ := mustOpen(t, dir)
	if err := s.Save(fullState()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("\n  \"version\": 1,")) {
		t.Fatalf("state.json is not indented:\n%s", b)
	}
}

func TestStoreModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, st := mustOpen(t, dir)
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("dir mode = %o, want 700", m)
	}
	for _, f := range []string{"state.json", "state.lock"} {
		if m := mode(t, filepath.Join(dir, f)); m != 0o600 {
			t.Errorf("%s mode = %o, want 600", f, m)
		}
	}
}

func TestStoreTmpCleanup(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "state.json.tmp-123")
	if err := os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, dir)
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("leftover temp file not removed: %v", err)
	}
}

func TestStoreQuarantine(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":   "{not json",
		"version0":  `{"version": 0, "prs": {}, "subs": {}}`,
		"noversion": `{"seq": 4}`,
		"badfield":  `{"version": 1, "seq": "nope"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			s, st := mustOpen(t, dir)
			if !reflect.DeepEqual(st, NewState()) {
				t.Fatalf("state after quarantine = %#v, want NewState", st)
			}
			path, at := s.Quarantine()
			if path == "" || at.IsZero() {
				t.Fatalf("Quarantine() = %q, %v; want path and time", path, at)
			}
			if !strings.HasPrefix(filepath.Base(path), "state.json.corrupt-") || filepath.Dir(path) != dir {
				t.Fatalf("quarantine path %q not state.json.corrupt-* in dir", path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != content {
				t.Fatalf("quarantined bytes = %q, want %q", b, content)
			}
			if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
				t.Fatalf("state.json still present after quarantine: %v", err)
			}
		})
	}
}

func TestStoreQuarantineNoClobber(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, _, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := s.Quarantine()
		paths = append(paths, p)
		_ = s.Close()
	}
	if paths[0] == paths[1] {
		t.Fatalf("second quarantine reused path %q", paths[0])
	}
	m, _ := filepath.Glob(filepath.Join(dir, "state.json.corrupt-*"))
	if len(m) != 2 {
		t.Fatalf("corrupt files = %v, want 2", m)
	}
}

func TestStoreNoQuarantineWhenClean(t *testing.T) {
	s, _ := mustOpen(t, t.TempDir())
	if p, at := s.Quarantine(); p != "" || !at.IsZero() {
		t.Fatalf("Quarantine() = %q, %v on a clean open", p, at)
	}
}

func TestStoreNewerVersion(t *testing.T) {
	dir := t.TempDir()
	content := `{"version": 2, "prs": {}, "subs": {}}`
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, st, err := Open(dir)
	if !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("Open err = %v, want ErrNewerVersion", err)
	}
	if s != nil || st != nil {
		t.Fatal("Open must return nil store and state on ErrNewerVersion")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Fatalf("error %q does not name the found version", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != content {
		t.Fatalf("state.json changed: %q, %v", b, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "state.json.corrupt-*")); len(m) != 0 {
		t.Fatalf("newer file was quarantined: %v", m)
	}
	// The lock must have been released.
	if err := os.WriteFile(path, []byte(`{"version": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, dir)
}

func TestStoreLocked(t *testing.T) {
	dir := t.TempDir()
	s1, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open err = %v, want ErrLocked", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, dir)
}

func TestStoreSaveFailureKeepsOldFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	s, _ := mustOpen(t, dir)
	want := fullState()
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.Save(NewState()); err == nil {
		t.Fatal("Save into a read-only dir succeeded")
	} else if !strings.HasPrefix(err.Error(), "store: save: ") {
		t.Fatalf("error %q not wrapped as store: save:", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, got := mustOpen(t, dir)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("previous state.json did not survive a failed Save")
	}
}

func TestStoreNilMapsBecomeEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version": 1, "seq": 9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, st := mustOpen(t, dir)
	if st.PRs == nil || st.Subs == nil || st.Seq != 9 {
		t.Fatalf("loaded state = %#v", st)
	}
}
