# Phase 5: Packaging, Plugin & Agent Skill - Discussion Log

> **Audit trail only.** Do not use this log as input to planning, research or execution agents.
> The decisions are captured in CONTEXT.md. This log keeps the alternatives that were considered.

**Date:** 2026-11-05
**Phase:** 05-packaging-plugin-agent-skill
**Areas discussed:** Plugin ↔ binary, Skill format & install, Release & Homebrew, Release gate & README

---

## Plugin ↔ binary

| Option | Description | Selected |
|--------|-------------|----------|
| Require PATH | No build step; the hook runs pr-relay from PATH | ✓ |
| [[build]] downloads release | Fetch the release binary into the plugin dir | |
| [[build]] go build | Build from the cloned source | |

| Option | Description | Selected |
|--------|-------------|----------|
| Subdir herdr-plugin/ | `herdr plugin install drodimin/pr-relay/herdr-plugin` | ✓ |
| Repo root | `herdr plugin install drodimin/pr-relay` | |

| Option | Description | Selected |
|--------|-------------|----------|
| Add `daemon --ensure` | Start in the background if needed, then exit; idempotent | ✓ |
| Reuse `pr-relay status` | Implicit auto-start | |
| No startup hook | CLI auto-start only | |

**Notes:** The user asked what the hook does. I explained that it is a one-shot run at herdr session start, and that it resumes polling of saved subscriptions after a reboot without any agent action.

| Option | Description | Selected |
|--------|-------------|----------|
| Startup hook only | Minimal plugin | |
| Add a herdr action | Status, List all subs, Restart daemon (all three chosen) | ✓ |

| Option | Description | Selected |
|--------|-------------|----------|
| Log + exit 0 | One-line plugin log message when the binary is missing | ✓ |
| Also show a toast | herdr notification | |

---

## Skill format & install

| Option | Description | Selected |
|--------|-------------|----------|
| One source, two forms | SKILL.md plus an AGENTS.md/CLAUDE.md snippet | ✓ |
| Snippet only | | |
| SKILL.md only | | |

| Option | Description | Selected |
|--------|-------------|----------|
| Embedded + `pr-relay skill` cmd | go:embed; print or install | ✓ |
| Files in repo only | | |
| Print only, no install | | |

**When to subscribe / on failure / trust.** The user answered in free text: "We should not give specific prescriptions… This plugin allows an agent to be subscribed to PR activity and do whatever they want in whatever scenarios they want." On failure: "The decision on what to do would lie with the agent. We just give back the reasons." On trust: "leave this to the agent and not over-engineer this part."

| Option | Description | Selected |
|--------|-------------|----------|
| One factual line + examples | Satisfy DIST-04 lightly and soften its wording | ✓ |
| Drop it, reword DIST-04 | Trust note in the README only | |

| Option | Description | Selected |
|--------|-------------|----------|
| Separate | Skill install only through the CLI | |
| Plugin action installs it | Fourth herdr action that runs `pr-relay skill install` | ✓ |

---

## Release & Homebrew

| Option | Description | Selected |
|--------|-------------|----------|
| Let research decide | Formula vs cask from current GoReleaser guidance; tap drodimin/homebrew-tap | ✓ |
| Formula | | |
| Cask | | |

| Option | Description | Selected |
|--------|-------------|----------|
| v0.1.0 on tag push | GoReleaser workflow | ✓ |
| v1.0.0 | | |

| Option | Description | Selected |
|--------|-------------|----------|
| No signing; document xattr | | ✓ |
| Notarize | | |

| Option | Description | Selected |
|--------|-------------|----------|
| Pipeline + snapshot only | The user pushes the tag | |
| Publish v0.1.0 in-phase | Human checkpoint, then tag, then end-to-end check | ✓ |

---

## Release gate & README

| Option | Description | Selected |
|--------|-------------|----------|
| Fold CR fixes into Phase 5 | CR-01..03 as the first plans | ✓ |
| Separate gap-closure first | | |
| Ship anyway | | |

| Option | Description | Selected |
|--------|-------------|----------|
| Yes, gate the tag on real UAT | | ✓ |
| No gate | | |

| Option | Description | Selected |
|--------|-------------|----------|
| Single README, task-first | | ✓ |
| README + docs/ folder | | |

| Option | Description | Selected |
|--------|-------------|----------|
| Golden-tested examples | | ✓ |
| Hand-written | | |

| Option | Description | Selected |
|--------|-------------|----------|
| Fold in the REQUIREMENTS drift fixes | | ✓ |
| No | | |

## Claude's Discretion

- The plugin manifest action syntax and the shell wrapper
- The default `skill install` directory
- `min_herdr_version`
- GoReleaser details
- LICENSE (MIT by default; flag it)
- `--ensure` output
- Plan ordering

## Deferred Ideas

None new.
