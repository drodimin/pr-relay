// Package testutil holds shared test helpers.
package testutil

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Golden compares got against the golden file at path (relative to the test's
// package directory, e.g. "testdata/scenarios/x/expected.golden").
// When the test binary runs with -update, the file is (re)written instead.
// Never pass -update in CI.
func Golden(t testing.TB, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden: mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("golden: write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden: read %s: %v (run `go test -update` to create it)", path, err)
	}
	if ok, msg := compare(got, want); !ok {
		t.Errorf("golden %s mismatch:\n%s", path, msg)
	}
}

// compare reports whether got equals want, with a got/want dump when not.
func compare(got, want []byte) (ok bool, msg string) {
	if bytes.Equal(got, want) {
		return true, ""
	}
	return false, fmt.Sprintf("--- got\n%s\n--- want\n%s", got, want)
}
