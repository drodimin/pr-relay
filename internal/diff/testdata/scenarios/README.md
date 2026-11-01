# Scenario fixtures

Real `PRState` GraphQL snapshots (query: `internal/ghquery/pr.graphql`) captured with
`scripts/capture.sh` from scratch PRs on the public dummy repo
[drodimin/pr-relay-fixtures](https://github.com/drodimin/pr-relay-fixtures), built from the template in
`scripts/fixture-repo/`. They were captured on 2026-11-01 (plan 01-07).

- `00-baseline.json` is the Baseline input. `NN-<what>.json` are the Step inputs, in order.
- `self.txt` holds the gh-authenticated login (`drodimin`) used for `filter.ExcludeSelf`.
- Every non-blank `body` was replaced with `PRRELAY-BODY-SENTINEL` by capture.sh
  (`TestDecodeAllFixtures` enforces this).
- `synthetic-*` directories are derived from real captures with jq. Every file in them carries a
  top-level `_synthetic` key that says what was changed. The decoder ignores it.

Actors:

| Actor | Kind | How |
|-------|------|-----|
| `drodimin` | User, gh-authenticated (self) | the active `gh` account |
| `dmitriy-medme` | User, second human (D-22) | `GH_TOKEN=$(gh auth token --user dmitriy-medme)` per command |
| `github-actions` | Bot (D-22) | `gh workflow run actor.yml` in the dummy repo, acting with `GITHUB_TOKEN` |

The dummy repo has two workflows on `pull_request`: `CI` (jobs `lint`, `test`, `flaky`, `slow`, `build`)
and `Other` (job `build`). Every real CI snapshot therefore contains the key collision
`CI/build` vs `Other/build`. `flaky` only runs on `flaky-*` branches and `slow` only on `slow-*`
branches. Everywhere else they report `SKIPPED`.

## Important: settle confirmation needs a replay

capture.sh writes a file only when the payload changes, so a settled CI state appears **once**.
`ci.settled` needs two consecutive identical all-completed snapshots. A real poller would see the
same payload again on the next poll. The scenario runner (plan 01-09) should therefore **replay the
last file once more** at the end of each scenario. In the tables below, the "replay" row is that
extra Step. The synthetic scenarios already contain their settle repeat as an explicit file, so
replaying them again emits nothing.

## Scenarios

| Scenario | Kind | PR | Actor(s) | Actions |
|----------|------|----|----------|---------|
| ci-fail-pass | real | #1 | drodimin | Open the PR with a `FAIL` file (CI/test fails), then push a commit removing it (all pass) |
| ci-flaky-rerun | real | #13 | drodimin | `flaky-*` branch: CI/flaky fails on attempt 1, then `gh run rerun --failed`, and attempt 2 passes |
| ci-cancelled | real | #3 | drodimin | `slow-*` branch: wait until everything but `slow` passed, then `gh run cancel` |
| push | real | #4 | drodimin | Push 2 commits in one push (both authored by self) |
| push-mixed-authors | real | #18 | drodimin (pusher), dmitriy-medme (author) | Push 2 commits in one push: one authored by the second account's noreply identity, one by self (extra scenario) |
| force-push | real | #6 | drodimin | `git commit --amend`, then `git push --force-with-lease` |
| conflict-on-off | real | #15 | drodimin | The PR edits src/x.go line 05. Push a conflicting edit of the same line to main, then merge main into the PR resolving it |
| mergeable-unknown | real | #16 | drodimin | Capture at 1 s, push a non-conflicting commit to main |
| merged | real | #17 | drodimin | `gh pr merge --merge` by self |
| closed | real | #8 | drodimin | `gh pr close` by self (D-20) |
| review-approved | real | #5 | dmitriy-medme | `gh pr review --approve`, no body |
| review-changes-requested-inline | real | #7 | dmitriy-medme | REQUEST_CHANGES review with a body and 3 inline comments (src/x.go x2, src/y.go x1) |
| review-commented | real | #9 | dmitriy-medme | (01) COMMENT review with a body and 1 inline. (02) COMMENT review via the reviews API with no body and 1 inline. (03) A "single comment" via `POST /pulls/N/comments` |
| issue-comment | real | #10 | dmitriy-medme | `gh pr comment`, then edit it with `PATCH /issues/comments/<id>`. The edit produced **no** new snapshot (see findings) |
| thread-reply | real | #11 | drodimin (seed), dmitriy-medme, drodimin | Seed: drodimin single inline comment (in the baseline). Then a second-account reply, then a self reply |
| bot-authors | real | #12 | github-actions[bot] | actor.yml: `comment`, `approve`, `review-inline`, `reply` (to the bot's own inline comment) |
| own-activity | real | #14 | drodimin, dmitriy-medme (seed) | Seed: second-account inline comment (in the baseline). Then self: issue comment, thread reply, COMMENTED review with 1 inline, push of 1 commit |
| synthetic-mergeable-unknown-conflicting | synthetic | #15 | - | 00 = conflict-on-off/00 with mergeable set to UNKNOWN. 01 = the real conflict-on-off/02 CONFLICTING. 02 = the same again (D-21) |
| synthetic-mergeable-unknown-mergeable | synthetic | #16 | - | 00 = mergeable-unknown/02 with mergeable set to UNKNOWN. 01 = the original MERGEABLE. 02 = UNKNOWN again (D-21) |
| synthetic-startup-failure | synthetic | #1 | - | 00 = ci-fail-pass/05 (running). 01 = ci-fail-pass/08 with CI/test set to STARTUP_FAILURE. 02 = the same again for settle |
| synthetic-many-failures | synthetic | #1 | - | 00 = ci-fail-pass/05 (running). 01 = ci-fail-pass/08 with CI/test expanded to 9 failing check runs `test-1`..`test-9` (distinct names, ids, databaseIds and check-run urls). 02 = the same again for settle (D-03) |

## Expected events

"diff" is what `diff.Step` should emit. "delivered" is what is left after `filter.ExcludeSelf(events, "drodimin")`.
Steps not listed emit nothing. The replay row is the extra Step on the last file (see above).
The current engine (after plans 01-04..01-06) produces exactly these events. This was checked with
a throwaway probe test on 2026-11-01.

### ci-fail-pass
| Step | diff | delivered |
|------|------|-----------|
| 03-test-failed | ci.failed CI/test x1 | same |
| 04-push-fix | commits x1 (count 1, authors [drodimin]) | nothing (self push) |
| replay of 08-ci-passed | ci.settled passed (6 checks) | same |

Note: 03 has every check COMPLETED once, but the head changes at 04 before any confirmation, so
the failed state of the first head never settles.

### ci-flaky-rerun
| Step | diff | delivered |
|------|------|-----------|
| 02-flaky-failed | ci.failed CI/flaky x1 | same |
| 03..05 | nothing (rerun in progress; no ci.recovered, D-08) | nothing |
| replay of 05-test-copied-settled | ci.settled passed (6 checks) | same |

### ci-cancelled
| Step | diff | delivered |
|------|------|-----------|
| 03-slow-cancelled | nothing (CANCELLED is neutral) | nothing |
| replay of 03 | ci.settled passed (6 checks) | same |

### push
| Step | diff | delivered |
|------|------|-----------|
| 01-push-2-commits-unknown | commits x1 (count 2, authors [drodimin, drodimin]) | nothing (all authors self) |
| replay of 03-ci-passed | ci.settled passed | same |

### push-mixed-authors
| Step | diff | delivered |
|------|------|-----------|
| 01-push-second-and-self | commits x1 (count 2, authors [dmitriy-medme, drodimin]) | same (one foreign author keeps it) |
| replay of 03-ci-passed | ci.settled passed | same |

### force-push
| Step | diff | delivered |
|------|------|-----------|
| 01-force-push-unknown | commits x1 (force-pushed, actor drodimin, count 0) | nothing (self force-push) |
| replay of 03-ci-passed | ci.settled passed | same |

### conflict-on-off
| Step | diff | delivered |
|------|------|-----------|
| 01-main-push-unknown | nothing (UNKNOWN, EVT-06) | nothing |
| 02-conflicting | conflict conflicting x1 | same |
| 03-merge-main-pushed-unknown | commits x1 (merge commit, authors [drodimin]) | nothing (self) |
| 04-mergeable | conflict cleared x1 (conflicting=false) | same |
| replay of 07-ci-passed | ci.settled passed | same |

### mergeable-unknown
| Step | diff | delivered |
|------|------|-----------|
| all steps and replay | nothing (MERGEABLE -> UNKNOWN -> MERGEABLE) | nothing |

### merged
| Step | diff | delivered |
|------|------|-----------|
| 01-merged-by-self | closed x1 (merged=true, by drodimin) | same, even though drodimin merged it (D-20) |

### closed
| Step | diff | delivered |
|------|------|-----------|
| 01-closed-by-self | closed x1 (merged=false, by drodimin) | same, even though drodimin closed it (D-20) |

### review-approved
| Step | diff | delivered |
|------|------|-----------|
| 01-approved-by-second | review APPROVED by dmitriy-medme (0 inline) x1 | same |

### review-changes-requested-inline
| Step | diff | delivered |
|------|------|-----------|
| 01-changes-requested-3-inline | review CHANGES_REQUESTED by dmitriy-medme (3 inline) x1, and no comment lines (D-10) | same |

### review-commented
| Step | diff | delivered |
|------|------|-----------|
| 01-commented-body-1-inline | review COMMENTED by dmitriy-medme (1 inline) x1 | same |
| 02-commented-nobody-1-inline | review COMMENTED by dmitriy-medme (1 inline) x1 | same |
| 03-single-comment | review COMMENTED by dmitriy-medme (1 inline) x1 (a non-reply inline makes it a real review) | same |

### issue-comment
| Step | diff | delivered |
|------|------|-----------|
| 01-comment-by-second | comment kind=comment by dmitriy-medme x1 | same |
| (edit) | no snapshot was produced. D-13 holds trivially | - |

### thread-reply
| Step | diff | delivered |
|------|------|-----------|
| 01-reply-by-second | comment kind=reply on src/x.go by dmitriy-medme x1 | same |
| 02-reply-by-self | comment kind=reply on src/x.go by drodimin x1 | nothing (self) |

### bot-authors
| Step | diff | delivered |
|------|------|-----------|
| 01-bot-comment | comment kind=comment by Bot github-actions x1 | same (bots are not self, D-12) |
| 02-bot-approved | review APPROVED by Bot github-actions (0 inline) x1 | same |
| 03-bot-review-inline | review COMMENTED by Bot github-actions (1 inline) x1 | same |
| 04-bot-reply | comment kind=reply on src/x.go by Bot github-actions x1 | same |

### own-activity
| Step | diff | delivered |
|------|------|-----------|
| 01-self-comment | comment kind=comment by drodimin x1 | nothing |
| 02-self-reply | comment kind=reply by drodimin x1 | nothing |
| 03-self-review-inline | review COMMENTED by drodimin (1 inline) x1 | nothing |
| 04-self-push-unknown | commits x1 (authors [drodimin]) | nothing |
| replay of 07-ci-passed | ci.settled passed | same (CI is never self activity) |

Invariant: no review, comment or commits event attributed to drodimin is delivered.

### synthetic-mergeable-unknown-conflicting (D-21)
| Step | diff | delivered |
|------|------|-----------|
| 01-conflicting | conflict conflicting x1 (D-21: the first resolved value after an UNKNOWN baseline is announced when it is CONFLICTING) | same |
| 02-conflicting-again | nothing | nothing |

### synthetic-mergeable-unknown-mergeable (D-21)
| Step | diff | delivered |
|------|------|-----------|
| all steps and replay | nothing. MERGEABLE is adopted silently and UNKNOWN emits nothing | nothing |

### synthetic-startup-failure
| Step | diff | delivered |
|------|------|-----------|
| 01-test-startup-failure | ci.failed CI/test x1 (STARTUP_FAILURE counts as failed) | same |
| 02-settle-repeat | ci.settled failed, failing [test] (6 checks) | same |

### synthetic-many-failures (D-03)
| Step | diff | delivered |
|------|------|-----------|
| 01-nine-failures | ci.failed x9 (CI/test-1..CI/test-9), one per check | same |
| 02-settle-repeat | ci.settled failed, failing [test-1..test-9] (14 checks) | same |

## Findings

Paths are relative to this directory. `PR` = `.data.repository.pullRequest`.

- **A1:** YES. A thread reply creates a new COMMENTED review with an empty body that wraps the
  reply. Evidence: `thread-reply/01-reply-by-second.json` `PR.reviews.nodes[1]` has state
  `COMMENTED`, body `""` and one comment whose `replyTo.id` is the seed comment in
  `PR.reviews.nodes[0].comments.nodes[0].id`. The self reply in `02-reply-by-self.json` (`nodes[2]`)
  behaves the same, and so do the bot reply (`bot-authors/04-bot-reply.json` `PR.reviews.nodes[2]`)
  and the self reply (`own-activity/02-self-reply.json` `PR.reviews.nodes[1]`).
- **A2:** NO, contexts list only the latest attempt. After `gh run rerun --failed`, the rollup holds
  only the newest attempt's check runs. The attempt-1 runs disappear and are not listed next to the
  rerun. Evidence: `ci-flaky-rerun/02-flaky-failed.json` `PR.statusCheckRollup.contexts` has 6 nodes, all
  `runAttempt` 1. `03-rerun-started.json` has `totalCount` 4: `Other/build` (attempt 1) plus `CI/flaky`,
  `CI/slow` and `CI/build` at attempt 2, each with a new `databaseId`. `CI/lint` and `CI/test` are
  **missing** in 03. They reappear at attempt 2 with new databaseIds in 04 and 05 (GitHub copies
  the non-failed jobs into the new attempt). So the diff must tolerate checks that briefly
  disappear during a rerun. The current engine does (no events at 03 and 04).
- **A3:** NO. In GraphQL, `Bot.login` does **not** include the `[bot]` suffix. Evidence:
  `bot-authors/01-bot-comment.json` `PR.comments.nodes[0].author` is
  `{"__typename":"Bot","login":"github-actions"}`. (The REST API shows the same actor as
  `github-actions[bot]`.)
- **Single comment:** YES. An inline "single comment" (`POST /pulls/N/comments`, not the reviews
  API) also creates a COMMENTED review with an empty body that wraps it. Evidence:
  `review-commented/03-single-comment.json` `PR.reviews.nodes[2]` has state `COMMENTED`, body `""` and one comment with
  `replyTo` null. The same holds for the reviews-API variant in `02-commented-nobody-1-inline.json`
  `PR.reviews.nodes[1]`.
- **UNKNOWN mergeable observed:** YES, in real captures. It appears after every head push
  (`push/01-push-2-commits-unknown.json`, `force-push/01-force-push-unknown.json`,
  `own-activity/04-self-push-unknown.json`, `conflict-on-off/03-merge-main-pushed-unknown.json`),
  after a push to the base branch (`mergeable-unknown/01-main-push-unknown.json`,
  `conflict-on-off/01-main-push-unknown.json`), at PR open (`ci-fail-pass/00-baseline.json`) and
  right after a merge (`merged/01-merged-by-self.json`, state MERGED with mergeable UNKNOWN). It is
  not guaranteed, though: `push-mixed-authors/01-push-second-and-self.json` stayed MERGEABLE across a head push.
- **A8 (github-actions[bot] approvals):** YES. With `can_approve_pull_request_reviews=true` on the
  repo, `gh pr review --approve` from a workflow using `GITHUB_TOKEN` worked. Evidence:
  `bot-authors/02-bot-approved.json` `PR.reviews.nodes[0]` is `APPROVED` by
  `{"__typename":"Bot","login":"github-actions"}`.

Other observations:

- **Issue-comment edits are invisible to the query.** The comments selection has no body or
  `updatedAt`, so editing a comment does not change the payload, and capture.sh wrote nothing.
  D-13 ("edits are ignored") is met by construction. There is no edit fixture.
- **The rollup lags the head.** After a push the first snapshot has `statusCheckRollup: null` for the new
  head (for example `push/01`), and the next lists only some checks (`ci-fail-pass/01` has 4 of 6).
  `SKIPPED` jobs register a few seconds later. So "all present checks completed" is not enough
  to settle, which is why settle needs confirmation.
- **`baseRefOid` does not follow the base branch.** It stays at the old value after main moves
  (`conflict-on-off/01..02` keep `30b9f17` while main advanced), and changes only when the PR
  branch merges main (`conflict-on-off/03` -> `493e966`).
- **merged also carries a ClosedEvent.** `merged/01-merged-by-self.json` has `merged: true`, `mergedBy`
  drodimin and a `ClosedEvent` by drodimin in `timelineItems`.
- **Commit authors resolve by email.** `push-mixed-authors/01` shows commit `5000c77` with author
  `dmitriy-medme` (noreply email) and committer `drodimin`.
- **The query costs 1 point** (`rateLimit.cost` 1, `nodeCount` 790) for the full PRState query.
