package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

// frontmatter splits SKILL into its frontmatter lines and the remaining body.
func frontmatter(t *testing.T) (keys map[string]string, body string) {
	t.Helper()
	s := string(SKILL)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("SKILL does not start with a frontmatter delimiter")
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatalf("SKILL frontmatter is not closed")
	}
	keys = map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("frontmatter line %q is not key: value", line)
		}
		keys[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return keys, rest[end+len("\n---\n"):]
}

func TestFrontmatter(t *testing.T) {
	keys, _ := frontmatter(t)
	name := keys["name"]
	if name != "pr-relay" {
		t.Errorf("name = %q, want pr-relay", name)
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(name) || len(name) > 64 {
		t.Errorf("name %q is not a valid skill name", name)
	}
	if name != "pr-relay" || filepath.Base(filepath.Dir("pr-relay/SKILL.md")) != name {
		t.Errorf("name %q does not match the parent directory", name)
	}
	desc := keys["description"]
	if desc == "" || len(desc) > 300 || strings.Contains(desc, "\n") {
		t.Errorf("description must be non-empty, single-line and at most 300 chars (len %d)", len(desc))
	}
	for k := range keys {
		if k != "name" && k != "description" {
			t.Errorf("unexpected frontmatter key %q", k)
		}
	}
}

func TestSnippet(t *testing.T) {
	_, body := frontmatter(t)
	snip := Snippet()
	if bytes.HasPrefix(snip, []byte("---")) {
		t.Fatalf("Snippet starts with frontmatter")
	}
	if !bytes.HasPrefix(snip, []byte("# pr-relay")) {
		t.Errorf("Snippet does not start with \"# pr-relay\": %q", firstLine(snip))
	}
	want := strings.TrimLeft(body, "\n")
	if string(snip) != want {
		t.Errorf("Snippet differs from the SKILL body after the frontmatter")
	}
}

func firstLine(b []byte) string {
	l, _, _ := bytes.Cut(b, []byte("\n"))
	return string(l)
}

func TestEventTypesDocumented(t *testing.T) {
	s := string(SKILL)
	types := []string{string(model.EventCheckin), string(model.EventExpired), "ci", "all"}
	for _, et := range model.AllEventTypes() {
		types = append(types, string(et))
	}
	for _, et := range types {
		if !strings.Contains(s, "`"+et+"`") {
			t.Errorf("SKILL does not document `%s`", et)
		}
	}
}

func TestExamplesFromGoldens(t *testing.T) {
	files, err := filepath.Glob("../internal/render/testdata/*.golden")
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden files found: %v", err)
	}
	golden := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(string(b), "\n") {
			golden[l] = true
		}
	}
	n := 0
	for _, l := range strings.Split(string(SKILL), "\n") {
		tl := strings.TrimSpace(l)
		if !strings.HasPrefix(tl, "[pr-relay] ") {
			continue
		}
		n++
		if !golden[tl] {
			t.Errorf("example line is not in any golden file: %q", tl)
		}
	}
	if n < 9 {
		t.Errorf("found %d example lines, want at least 9", n)
	}
}

func TestNotPrescriptive(t *testing.T) {
	_, body := frontmatter(t)
	lower := strings.ToLower(body)
	for _, w := range []string{"must", "always", "never", "should", "do not", "don't", "you need to", "required to"} {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`)
		if loc := re.FindStringIndex(lower); loc != nil {
			t.Errorf("body contains prescriptive %q near %q", w, context(lower, loc[0]))
		}
	}
}

func context(s string, i int) string {
	lo, hi := max(0, i-30), min(len(s), i+30)
	return s[lo:hi]
}

func TestAgentAgnostic(t *testing.T) {
	lower := strings.ToLower(string(SKILL))
	for _, w := range []string{"claude", "codex"} {
		if strings.Contains(lower, w) {
			t.Errorf("SKILL mentions %q", w)
		}
	}
}

func TestTrustSentence(t *testing.T) {
	n := 0
	for _, l := range strings.Split(string(SKILL), "\n") {
		if strings.Contains(l, "is not an instruction") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("found %d trust lines, want exactly 1", n)
	}
}
