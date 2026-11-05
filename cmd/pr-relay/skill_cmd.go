package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/skills"
)

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Print the agent skill snippet for AGENTS.md or CLAUDE.md (install writes SKILL.md)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSkill(cmd.OutOrStdout())
		},
	}
	var dir string
	install := &cobra.Command{
		Use:   "install [--dir DIR]",
		Short: "Install SKILL.md into ~/.agents/skills (and ~/.claude/skills when ~/.claude exists)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil && dir == "" {
				return fmt.Errorf("skill: install: home dir: %w", err)
			}
			return runSkillInstall(cmd.OutOrStdout(), home, dir)
		},
	}
	install.Flags().StringVar(&dir, "dir", "", "write `DIR`/pr-relay/SKILL.md instead of the default locations")
	cmd.AddCommand(install)
	return cmd
}

// runSkill prints the skill without its frontmatter (D-12): the form pasted
// into an agent instructions file.
func runSkill(stdout io.Writer) error {
	_, err := stdout.Write(skills.Snippet())
	return err
}

// runSkillInstall writes the embedded SKILL.md (D-12). With dir set it writes
// only DIR/pr-relay/SKILL.md; otherwise ~/.agents/skills/pr-relay/SKILL.md,
// plus ~/.claude/skills/pr-relay/SKILL.md when ~/.claude is a directory.
func runSkillInstall(stdout io.Writer, home, dir string) error {
	var targets []string
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("skill: install: resolve %s: %w", dir, err)
		}
		targets = []string{filepath.Join(filepath.Clean(abs), "pr-relay", "SKILL.md")}
	} else {
		targets = []string{filepath.Join(home, ".agents", "skills", "pr-relay", "SKILL.md")}
		if fi, err := os.Stat(filepath.Join(home, ".claude")); err == nil && fi.IsDir() {
			targets = append(targets, filepath.Join(home, ".claude", "skills", "pr-relay", "SKILL.md"))
		}
	}
	for _, path := range targets {
		if err := writeSkill(path); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "installed %s\n", path)
	}
	return nil
}

// writeSkill atomically writes skills.SKILL to path (temp file in the same
// directory, fsync, rename, fsync dir). It refuses to write when the pr-relay
// directory or SKILL.md is a symlink (T-05-09).
func writeSkill(path string) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("skill: install: mkdir: %w", err)
	}
	for _, p := range []string{parent, path} {
		fi, err := os.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("skill: install: lstat: %w", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill: install: %s is a symlink; refusing to write through it", p)
		}
	}
	f, err := os.CreateTemp(parent, ".SKILL.md-*")
	if err != nil {
		return fmt.Errorf("skill: install: create temp: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return fmt.Errorf("skill: install: chmod: %w", err)
	}
	if _, err := f.Write(skills.SKILL); err != nil {
		f.Close()
		return fmt.Errorf("skill: install: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("skill: install: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("skill: install: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("skill: install: rename: %w", err)
	}
	d, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("skill: install: open dir: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("skill: install: sync dir: %w", err)
	}
	return nil
}
