package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/skills"
)

func TestSkillPrintsSnippet(t *testing.T) {
	var buf bytes.Buffer
	if err := runSkill(&buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), skills.Snippet()) {
		t.Errorf("runSkill output differs from skills.Snippet()")
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"skill"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), skills.Snippet()) {
		t.Errorf("`pr-relay skill` output differs from skills.Snippet()")
	}
}

// listFiles returns every regular file under root, relative to root.
func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func assertSkillFile(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, skills.SKILL) {
		t.Errorf("%s differs from the embedded SKILL", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("%s mode = %v, want 0644", path, fi.Mode().Perm())
	}
}

func outLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func TestSkillInstallDir(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	var out bytes.Buffer
	if err := runSkillInstall(&out, home, dir); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "pr-relay", "SKILL.md")
	assertSkillFile(t, want)
	if got := listFiles(t, dir); len(got) != 1 || got[0] != filepath.Join("pr-relay", "SKILL.md") {
		t.Errorf("files under dir = %v, want only pr-relay/SKILL.md", got)
	}
	if got := listFiles(t, home); len(got) != 0 {
		t.Errorf("--dir wrote under home: %v", got)
	}
	if out.String() != "installed "+want+"\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestSkillInstallRelativeDir(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	var out bytes.Buffer
	if err := runSkillInstall(&out, t.TempDir(), "rel"); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.EvalSymlinks(base)
	got := strings.TrimSuffix(strings.TrimPrefix(out.String(), "installed "), "\n")
	if !filepath.IsAbs(got) {
		t.Errorf("printed path %q is not absolute", got)
	}
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != filepath.Join(abs, "rel", "pr-relay", "SKILL.md") {
		t.Errorf("installed at %q, want under %q", got, abs)
	}
}

func TestSkillInstallDefaults(t *testing.T) {
	agents := func(home string) string { return filepath.Join(home, ".agents", "skills", "pr-relay", "SKILL.md") }
	claude := func(home string) string { return filepath.Join(home, ".claude", "skills", "pr-relay", "SKILL.md") }

	t.Run("no .claude", func(t *testing.T) {
		home := t.TempDir()
		var out bytes.Buffer
		if err := runSkillInstall(&out, home, ""); err != nil {
			t.Fatal(err)
		}
		assertSkillFile(t, agents(home))
		if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
			t.Errorf(".claude was created: %v", err)
		}
		if got := outLines(out.String()); len(got) != 1 || got[0] != "installed "+agents(home) {
			t.Errorf("output = %q", out.String())
		}
	})

	t.Run("with .claude", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runSkillInstall(&out, home, ""); err != nil {
			t.Fatal(err)
		}
		assertSkillFile(t, agents(home))
		assertSkillFile(t, claude(home))
		got := outLines(out.String())
		if len(got) != 2 || got[0] != "installed "+agents(home) || got[1] != "installed "+claude(home) {
			t.Errorf("output = %q", out.String())
		}
	})
}

func TestSkillInstallOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pr-relay", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runSkillInstall(&bytes.Buffer{}, t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	assertSkillFile(t, path)
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".SKILL.md-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("entries = %d, want 1", len(entries))
	}
}

func TestSkillInstallRefusesSymlink(t *testing.T) {
	t.Run("SKILL.md symlink", func(t *testing.T) {
		dir, outside := t.TempDir(), t.TempDir()
		target := filepath.Join(outside, "victim")
		if err := os.WriteFile(target, []byte("keep me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "pr-relay"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "pr-relay", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		err := runSkillInstall(&bytes.Buffer{}, t.TempDir(), dir)
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("err = %v, want symlink refusal", err)
		}
		if b, _ := os.ReadFile(target); string(b) != "keep me\n" {
			t.Errorf("symlink target changed: %q", b)
		}
	})

	t.Run("pr-relay dir symlink", func(t *testing.T) {
		dir, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "pr-relay")); err != nil {
			t.Fatal(err)
		}
		err := runSkillInstall(&bytes.Buffer{}, t.TempDir(), dir)
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("err = %v, want symlink refusal", err)
		}
		if got := listFiles(t, outside); len(got) != 0 {
			t.Errorf("wrote through symlinked dir: %v", got)
		}
	})
}
