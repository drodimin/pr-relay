// Package skills holds the agent-facing pr-relay skill. There is exactly one
// source file, pr-relay/SKILL.md, embedded into the binary so the text agents
// read always matches the installed version.
package skills

import (
	"bytes"
	_ "embed"
)

// SKILL is the full SKILL.md, including its Agent Skills frontmatter.
//
//go:embed pr-relay/SKILL.md
var SKILL []byte

// Snippet returns SKILL without its leading "---" frontmatter block and
// without the blank lines that follow it: the form pasted into AGENTS.md or
// similar files. SKILL is returned unchanged when it has no frontmatter.
func Snippet() []byte {
	const delim = "---\n"
	if !bytes.HasPrefix(SKILL, []byte(delim)) {
		return SKILL
	}
	rest := SKILL[len(delim):]
	end := bytes.Index(rest, []byte("\n"+delim))
	if end < 0 {
		return SKILL
	}
	return bytes.TrimLeft(rest[end+len("\n"+delim):], "\n")
}
