package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/model"
)

// D-24: README.md and SKILL.md follow the binary. Example lines must be
// verbatim golden lines (the goldens are the source of truth and are never
// regenerated from the docs), and every command, flag, event type and config
// key must be documented.

const (
	readmePath = "../../README.md"
	skillPath  = "../../skills/pr-relay/SKILL.md"
)

// exampleSpanRE matches an inline code span holding a relay line.
var exampleSpanRE = regexp.MustCompile("`(\\[pr-relay\\] [^`]+)`")

// exampleSpans returns every relay example in doc: whole lines starting with
// "[pr-relay] " (after trimming) and inline code spans, including Markdown
// table cells, whose content starts with "[pr-relay] ".
func exampleSpans(doc string) []string {
	var out []string
	for _, l := range strings.Split(doc, "\n") {
		if tl := strings.TrimSpace(l); strings.HasPrefix(tl, "[pr-relay] ") {
			out = append(out, tl)
		}
	}
	for _, m := range exampleSpanRE.FindAllStringSubmatch(doc, -1) {
		out = append(out, m[1])
	}
	return out
}

func goldenUnion(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("../../internal/render/testdata/*.golden")
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
			if l != "" {
				golden[l] = true
			}
		}
	}
	return golden
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("README/SKILL doc missing: %v", err)
	}
	return string(b)
}

// documentedCommands walks the cobra tree, skipping hidden commands and
// cobra's help and completion.
func documentedCommands(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		out = append(out, sub)
		out = append(out, documentedCommands(sub)...)
	}
	return out
}

func eventTypes() []string {
	var out []string
	for _, et := range model.AllEventTypes() {
		out = append(out, string(et))
	}
	return append(out, string(model.EventCheckin), string(model.EventExpired))
}

func tomlName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	return name
}

func TestDocsDrift(t *testing.T) {
	golden := goldenUnion(t)

	t.Run("examples_catch_table_drift", func(t *testing.T) {
		doc := "| Type | Meaning | Example |\n|---|---|---|\n| `ci.failed` | x | `[pr-relay] this line is not golden` |\n"
		spans := exampleSpans(doc)
		const want = "[pr-relay] this line is not golden"
		found := false
		for _, s := range spans {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("exampleSpans(%q) = %q, missing the table-cell span", doc, spans)
		}
		if golden[want] {
			t.Fatalf("self-check span %q is unexpectedly golden", want)
		}
	})

	t.Run("examples", func(t *testing.T) {
		for _, path := range []string{readmePath, skillPath} {
			doc := readDoc(t, path)
			spans := exampleSpans(doc)
			for _, s := range spans {
				if !golden[s] {
					t.Errorf("%s: example is not a golden line: %q", path, s)
				}
			}
			if path == readmePath && len(spans) < 9 {
				t.Errorf("%s: found %d examples, want at least 9", path, len(spans))
			}
		}
	})

	t.Run("events", func(t *testing.T) {
		readme := readDoc(t, readmePath)
		skill := readDoc(t, skillPath)
		lines := strings.Split(readme, "\n")
		for _, et := range eventTypes() {
			if !strings.Contains(skill, "`"+et+"`") {
				t.Errorf("SKILL.md does not document event type `%s`", et)
			}
			prefix := "| `" + et + "` |"
			var row string
			for _, l := range lines {
				if strings.HasPrefix(strings.TrimSpace(l), prefix) {
					row = l
					break
				}
			}
			if row == "" {
				t.Errorf("README.md has no Events table row for `%s`", et)
				continue
			}
			ms := exampleSpanRE.FindAllStringSubmatch(row, -1)
			if len(ms) == 0 {
				t.Errorf("README.md Events row for `%s` has no [pr-relay] example", et)
			}
			for _, m := range ms {
				if !golden[m[1]] {
					t.Errorf("README.md Events row for `%s`: example is not a golden line: %q", et, m[1])
				}
			}
		}
	})

	cmds := documentedCommands(newRootCmd())

	t.Run("commands", func(t *testing.T) {
		readme := readDoc(t, readmePath)
		skill := readDoc(t, skillPath)
		for _, c := range cmds {
			p := "pr-relay " + strings.TrimPrefix(c.CommandPath(), "pr-relay ")
			if !strings.Contains(readme, p) {
				t.Errorf("README.md does not document %q", p)
			}
			if !strings.Contains(skill, p) {
				t.Errorf("SKILL.md does not document %q", p)
			}
		}
	})

	t.Run("flags", func(t *testing.T) {
		readme := readDoc(t, readmePath)
		skill := readDoc(t, skillPath)
		for _, c := range cmds {
			c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
				if f.Hidden || f.Name == "help" {
					return
				}
				n := "--" + f.Name
				if !strings.Contains(readme, n) {
					t.Errorf("README.md does not document %s of %q", n, c.CommandPath())
				}
				if !strings.Contains(skill, n) {
					t.Errorf("SKILL.md does not document %s of %q", n, c.CommandPath())
				}
			})
		}
	})

	t.Run("config", func(t *testing.T) {
		readme := readDoc(t, readmePath)
		ct := reflect.TypeOf(config.Config{})
		for i := 0; i < ct.NumField(); i++ {
			tf := ct.Field(i)
			table := tomlName(tf)
			if !strings.Contains(readme, "["+table+"]") {
				t.Errorf("README.md does not document config table [%s]", table)
			}
			for j := 0; j < tf.Type.NumField(); j++ {
				key := tomlName(tf.Type.Field(j))
				if !strings.Contains(readme, "`"+key+"`") {
					t.Errorf("README.md does not document config key [%s] `%s`", table, key)
				}
			}
		}
	})

	t.Run("sections", func(t *testing.T) {
		readme := readDoc(t, readmePath)
		if !strings.HasPrefix(readme, "# pr-relay") {
			t.Errorf("README.md does not start with \"# pr-relay\"")
		}
		var heads []string
		for _, l := range strings.Split(readme, "\n") {
			if strings.HasPrefix(l, "## ") {
				heads = append(heads, strings.TrimPrefix(l, "## "))
			}
		}
		want := []string{"Quickstart", "Commands", "Events", "Configuration", "How it works", "Threat model", "Troubleshooting"}
		i := 0
		for _, h := range heads {
			if i < len(want) && strings.HasPrefix(h, want[i]) {
				i++
			}
		}
		if i != len(want) {
			t.Errorf("README.md ## headings %q do not contain %q in order (matched %d)", heads, want, i)
		}
	})

	t.Run("install", func(t *testing.T) {
		readme := readDoc(t, readmePath)
		for _, s := range []string{
			"brew install drodimin/tap/pr-relay",
			"go install github.com/drodimin/pr-relay/cmd/pr-relay@latest",
			"herdr plugin install drodimin/pr-relay/herdr-plugin",
			"pr-relay skill install",
			"xattr -d com.apple.quarantine",
			"prompt injection",
		} {
			if !strings.Contains(readme, s) {
				t.Errorf("README.md does not contain %q", s)
			}
		}
		if strings.Contains(strings.ToLower(readme), "homebrew avoids quarantine") {
			t.Errorf("README.md claims Homebrew avoids quarantine")
		}
	})
}
