package diff_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/diff"
	"github.com/drodimin/pr-relay/internal/filter"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/render"
	"github.com/drodimin/pr-relay/internal/testutil"
)

const scenariosDir = "testdata/scenarios"

// stepOutput is what one Step of a scenario produced after filtering.
type stepOutput struct {
	File     string        // file name without .json; "replay <name>" for the replay step
	Raw      []model.Event // diff.Step output before ExcludeSelf
	Events   []model.Event // delivered events (after ExcludeSelf)
	Lines    []string      // render.Lines(Snapshot.Ref, Events)
	Excluded []string      // rendered events ExcludeSelf removed, without the tag
	Snapshot model.Snapshot
	Gap      bool
}

// readSelf returns the gh-authenticated login used for ExcludeSelf.
func readSelf(t testing.TB) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(scenariosDir, "self.txt"))
	if err != nil {
		t.Fatalf("read self.txt: %v", err)
	}
	self := strings.TrimSpace(string(b))
	if self == "" {
		t.Fatal("self.txt is empty")
	}
	return self
}

// scenarioDirs lists every scenario directory, sorted by name.
func scenarioDirs(t testing.TB) []string {
	t.Helper()
	entries, err := os.ReadDir(scenariosDir)
	if err != nil {
		t.Fatalf("read %s: %v", scenariosDir, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		t.Fatal("no scenario directories found")
	}
	return dirs
}

// runScenario feeds dir's snapshots through the whole pure core:
// Decode -> Baseline (first file) -> Step -> ExcludeSelf -> render.Lines.
// The golden also lists, untagged and indented, any events ExcludeSelf
// removed; they are never delivered.
//
// capture.sh only writes a file when the payload changes, so a settled CI
// state appears once in a real capture, while ci.settled needs two identical
// consecutive snapshots. A real poller would see the same payload again on its
// next poll, so the last file is replayed once more as a final "replay" step
// (see testdata/scenarios/README.md). Synthetic scenarios already contain
// their settle repeat; replaying them again must emit nothing, which the
// exactly-once invariant checks.
func runScenario(t testing.TB, dir, self string) []stepOutput {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	sort.Strings(files)
	if len(files) < 2 {
		t.Fatalf("%s: need a baseline plus at least one step, have %d files", dir, len(files))
	}
	snaps := make([]model.Snapshot, len(files))
	for i, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		s, _, err := ghquery.Decode(data)
		if err != nil {
			t.Fatalf("decode %s: %v", f, err)
		}
		snaps[i] = s
	}

	mem := diff.Baseline(snaps[0])
	var out []stepOutput
	step := func(name string, s model.Snapshot) {
		r := diff.Step(mem, s)
		mem = r.Memory
		evs := filter.ExcludeSelf(r.Events, self)
		out = append(out, stepOutput{
			File:     name,
			Raw:      r.Events,
			Events:   evs,
			Excluded: excludedLines(s.Ref, r.Events, evs),
			Lines:    render.Lines(s.Ref, evs),
			Snapshot: s,
			Gap:      r.Gap,
		})
	}
	for i := 1; i < len(files); i++ {
		step(strings.TrimSuffix(filepath.Base(files[i]), ".json"), snaps[i])
	}
	last := len(files) - 1
	step("replay "+strings.TrimSuffix(filepath.Base(files[last]), ".json"), snaps[last])
	return out
}

// excludedLines renders the events in raw that are missing from delivered.
// The golden lists them (untagged) so reviewers see what own-activity
// filtering suppressed.
func excludedLines(ref model.PRRef, raw, delivered []model.Event) []string {
	kept := make(map[string]bool, len(delivered))
	for _, e := range delivered {
		kept[e.Key] = true
	}
	var gone []model.Event
	for _, e := range raw {
		if !kept[e.Key] {
			gone = append(gone, e)
		}
	}
	lines := render.Lines(ref, gone)
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "[pr-relay] ")
	}
	return lines
}

func TestScenarios(t *testing.T) {
	self := readSelf(t)
	for _, name := range scenarioDirs(t) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(scenariosDir, name)
			var buf bytes.Buffer
			for _, so := range runScenario(t, dir, self) {
				buf.WriteString("step " + so.File + ":\n")
				for _, l := range so.Lines {
					buf.WriteString(l + "\n")
				}
				for _, l := range so.Excluded {
					buf.WriteString("  excluded (self): " + l + "\n")
				}
				if so.Gap {
					buf.WriteString("  (gap)\n")
				}
			}
			testutil.Golden(t, filepath.Join(dir, "expected.golden"), buf.Bytes())
		})
	}
}
