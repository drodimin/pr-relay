---
quick_id: 261116-rqq
status: complete
files_modified: [README.md]
---

# Summary: README intro and Quick start rewrite

- Replaced the dense opening paragraph with a tagline, a problem/solution paragraph, a five-point
  benefits list and a "what the agent sees" example using the real `sub` output format
  (from cmd/pr-relay/format.go) and real event lines from the Events table.
- Quick start is now one copy-paste block (go install, plugin install, skill install), one PATH and
  herdr restart line, and a pointer to the alternative.
- Release download, checksum verification, the single-copy warning, the plugin startup hook and the
  skill install details moved to a new "Installation details" section. No facts were dropped.
- The #troubleshooting and #events anchors still resolve.
