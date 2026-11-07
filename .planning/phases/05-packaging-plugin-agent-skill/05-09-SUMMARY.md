---
phase: 05-packaging-plugin-agent-skill
plan: 09
subsystem: release
tags: [goreleaser, ci, release, homebrew]
requires:
  - "05-05: cmd/pr-relay main.version ldflags target + ReadBuildInfo fallback"
  - "05-08: README install/quarantine wording pinned by TestDocsDrift"
provides:
  - ".goreleaser.yaml (v2): 4 static targets, tar.gz archives, checksums.txt, grouped changelog, homebrew_casks to drodimin/homebrew-tap"
  - ".github/workflows/release.yml: v* tag-only release job"
  - ".github/workflows/ci.yml goreleaser job: check + snapshot on every push/PR"
affects:
  - "05-10: append LICENSE to archives.files once the license is chosen"
  - "Release UAT: brew install drodimin/tap/pr-relay then `pr-relay version` on macOS (cask postflight xattr)"
tech-stack:
  added: ["GoReleaser v2 (CI via goreleaser-action@v7, local via go run ...@v2.20.0)"]
  patterns:
    - "Cask quarantine strip via hooks.post.install_steps on_macos run /usr/bin/xattr (renders as Homebrew postflight_steps with {{staged_path}})"
key-files:
  created:
    - .goreleaser.yaml
    - .github/workflows/release.yml
  modified:
    - .github/workflows/ci.yml
    - README.md
decisions:
  - "homebrew_casks (not brews) with on_macos xattr postflight step; GoReleaser v2.20.0 check reports no deprecations for any key used"
  - "Archive name pr-relay_<version>_<os>_<arch>.tar.gz (version without v); README release-download step now names this pattern"
  - "CI snapshot job sets HOMEBREW_TAP_GITHUB_TOKEN to literal snapshot-unused; the real secret appears only in the tag-triggered release.yml"
metrics:
  duration: "~15m"
  completed: 2026-11-07
  tasks: 2
  files: 4
---

# Phase 5 Plan 09: GoReleaser Release Pipeline Summary

The GoReleaser v2 config builds four `CGO_ENABLED=0` binaries (darwin and linux, each on amd64 and arm64) stamped with `pr-relay v<version>`, writes `checksums.txt` and pushes a quarantine-stripping Homebrew cask to `drodimin/homebrew-tap`. A tag-only release workflow runs it, and a CI job checks the config and does a snapshot build on every push and PR. A local snapshot build proved it all.

## Tasks

| Task | Name | Commit | Files |
| ---- | ---- | ------ | ----- |
| 1 | .goreleaser.yaml and local snapshot proof | b017cf4 | .goreleaser.yaml, README.md |
| 2 | release.yml and CI snapshot job | 04717f6 | .github/workflows/release.yml, .github/workflows/ci.yml |

## Local snapshot evidence (GoReleaser v2.20.0)

- `goreleaser check`: "1 configuration file(s) validated", with no deprecation warnings. The deprecated forms are not used: no `brews`, no singular `format`/`binary`, no snapshot `name_template`, and no old `install` hook.
- `release --snapshot --clean --skip=publish` produced the four `dist/pr-relay_0.0.1-next_{darwin,linux}_{amd64,arm64}.tar.gz` archives plus `checksums.txt`. Each archive holds `pr-relay` and `README.md`.
- `file` on the linux amd64 binary: "ELF 64-bit LSB executable, x86-64, statically linked, stripped".
- The darwin arm64 binary run with `version` prints `pr-relay v0.0.1-next`, so the leading `v` from the ldflags works.
- `dist/homebrew/Casks/pr-relay.rb` contains `postflight_steps do on_macos do run "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "{{staged_path}}/pr-relay"]`. GoReleaser turns `{{ .StagedPath }}` into Homebrew's `{{staged_path}}` placeholder. The local Homebrew 7.0.11 source (`cask/artifact/install_steps.rb`) handles `staged_path` interpolation and allows `run` and `on_macos` in steps blocks.
- `git status --porcelain dist` is empty, because `/dist/` is already gitignored.
- `go test ./cmd/pr-relay -run TestDocsDrift` passes, and so does the full `go test ./...`.

## Carry-forward from 05-08 resolved

- The cask's post-install quarantine strip exists, so the README sentence "The Homebrew cask removes the quarantine attribute in its post-install step" is true and stays unchanged.
- The README release-download step now names the real archive pattern `pr-relay_<version>_<os>_<arch>.tar.gz`, with `pr-relay_0.1.0_darwin_arm64.tar.gz` as the example. TestDocsDrift stays green.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] README archive naming reconciled (carry-forward from 05-08)**
- **Found during:** Task 1
- **Issue:** The README used a generic phrase for the archive name because no GoReleaser config existed yet.
- **Fix:** Named the real `name_template` pattern in the release-download bullet.
- **Files modified:** README.md
- **Commit:** b017cf4

**2. Comment wording adjusted to satisfy grep acceptance checks**
- A comment that said "never `go mod tidy`" in .goreleaser.yaml and one that said "never add pull_request" in release.yml made the `grep -c` checks count non-zero. I reworded both comments and left the behaviour unchanged.

### Observations (not fixed)

- **otool on the darwin arm64 binary** lists `/usr/lib/libSystem.B.dylib` plus `/usr/lib/libresolv.9.dylib`, `CoreFoundation.framework` and `Security.framework`. It does not list libSystem alone, as the plan's action text expected. All four are OS-shipped system libraries. With `CGO_ENABLED=0`, Go on darwin links them for DNS (`net`) and root certificates (`crypto/x509`) through its non-cgo syscall trampolines. Nothing outside the OS is linked, so the binary is still "static" in the DIST-01 sense of no cgo and no third-party dylibs (Pitfall 6). No change was needed.
- **`brew style` on the generated cask** refused to run because Homebrew 7 only lints casks that live in a tap. I did not create a local tap, since that would change the user's Homebrew state. The real proof is the release UAT: `brew install drodimin/tap/pr-relay` followed by `pr-relay version` on macOS.

## Threat model compliance

- T-05-25: `secrets.HOMEBREW_TAP_GITHUB_TOKEN` appears only in release.yml, which runs on `v*` tag pushes only. The CI job uses the literal `snapshot-unused`.
- T-05-26: release.yml grants `contents: write` and nothing else. ci.yml stays `contents: read`, and the count of `contents: write` in it is 0.
- T-05-SC: CI uses the official `goreleaser/goreleaser-action@v7` with `version: "~> v2"`, and the local check is pinned to v2.20.0.
- T-05-28: the before hook is only `go mod download`.

## Known Stubs

None. `archives.files` deliberately lists only README.md. Plan 05-10 adds LICENSE.

## Self-Check: PASSED

- FOUND: .goreleaser.yaml, .github/workflows/release.yml, .github/workflows/ci.yml (goreleaser job)
- FOUND commits: b017cf4, 04717f6
