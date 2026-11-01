package ghquery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// bodySentinel is what scripts/capture.sh writes in place of any non-blank body.
const bodySentinel = "PRRELAY-BODY-SENTINEL"

// minFixtures is the smallest scenario fixture set plan 01-07 produces.
const minFixtures = 20

func scenarioFixtures(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../diff/testdata/scenarios/*/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < minFixtures {
		t.Fatalf("found %d scenario fixtures, want at least %d", len(files), minFixtures)
	}
	return files
}

// badBodies walks a generic JSON value and returns the path of every "body"
// key whose value is not "" or the sentinel.
func badBodies(v any, path string, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := path + "." + k
			if k == "body" {
				s, ok := child.(string)
				if !ok || (s != "" && s != bodySentinel) {
					*out = append(*out, p)
				}
				continue
			}
			badBodies(child, p, out)
		}
	case []any:
		for i, child := range x {
			badBodies(child, path+"["+strconv.Itoa(i)+"]", out)
		}
	}
}

func TestDecodeAllFixtures(t *testing.T) {
	files := scenarioFixtures(t)
	t.Logf("checking %d fixtures", len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s, _, err := Decode(raw)
		if err != nil {
			t.Errorf("%s: Decode: %v", f, err)
			continue
		}
		if !s.Ref.Valid() {
			t.Errorf("%s: Ref %+v is not valid", f, s.Ref)
		}
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Errorf("%s: generic unmarshal: %v", f, err)
			continue
		}
		var bad []string
		badBodies(generic, "$", &bad)
		for _, p := range bad {
			t.Errorf("%s: %s holds a body other than \"\" or the sentinel", f, p)
		}
		if s.CIKnown {
			for _, c := range s.Checks {
				if c.Key == "" {
					t.Errorf("%s: check %q has an empty Key", f, c.Name)
				}
			}
		}
	}
}

func TestFixturesNoStatusContextLeak(t *testing.T) {
	for _, f := range scenarioFixtures(t) {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s, _, err := Decode(raw)
		if err != nil {
			t.Errorf("%s: Decode: %v", f, err)
			continue
		}
		for _, c := range s.Checks {
			if c.DatabaseID == 0 {
				t.Errorf("%s: check %q has no DatabaseID (StatusContext leak?)", f, c.Key)
			}
		}
	}
}
