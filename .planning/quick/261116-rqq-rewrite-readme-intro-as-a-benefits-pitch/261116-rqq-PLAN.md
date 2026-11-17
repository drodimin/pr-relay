---
quick_id: 261116-rqq
type: quick
files_modified: [README.md]
---

# Rewrite README intro as a benefits pitch and streamline Quick start

## Task 1: Rewrite the opening section
- Replace the single dense paragraph with a one-line tagline, a short problem/solution paragraph,
  a scannable benefits list, and a tiny "what it looks like" example.
- Keep claims accurate to the implementation (idle-only delivery, own-activity filtering,
  one poll per PR, gh auth reuse, agent-agnostic).

## Task 2: Streamline Quick start
- One install path (`go install`) plus the plugin and skill install as one copy-paste block.
- Move release-download instructions, checksum verification, the single-copy warning and
  skill install details into a new "Installation details" section below Quick start.
- Keep the herdr PATH/restart caveat as one short line.

## Verify
- All anchors referenced (#troubleshooting, #events) still resolve.
- No details lost: every removed fact from the old Quickstart appears in Installation details.
