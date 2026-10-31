package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	if ok, msg := compare([]byte("a\n"), []byte("a\n")); !ok || msg != "" {
		t.Errorf("equal inputs: ok=%v msg=%q, want ok=true msg=\"\"", ok, msg)
	}
	ok, msg := compare([]byte("got-text"), []byte("want-text"))
	if ok {
		t.Fatal("different inputs: ok=true, want false")
	}
	for _, s := range []string{"--- got", "got-text", "--- want", "want-text"} {
		if !strings.Contains(msg, s) {
			t.Errorf("mismatch msg missing %q:\n%s", s, msg)
		}
	}
}

func TestGoldenMatch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x", "expected.golden")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Golden(t, p, []byte("hello\n"))
}

func TestGoldenUpdateWrites(t *testing.T) {
	old := *update
	*update = true
	t.Cleanup(func() { *update = old })

	p := filepath.Join(t.TempDir(), "nested", "dir", "out.golden")
	Golden(t, p, []byte("fresh\n"))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("golden file not written: %v", err)
	}
	if string(b) != "fresh\n" {
		t.Errorf("written = %q, want %q", b, "fresh\n")
	}
}
