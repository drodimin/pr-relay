// Package poller decides when each watched PR is polled and how much of the
// GitHub rate-limit budget pr-relay may spend doing it.
//
// cadence.go and budget.go are pure: every time is passed in as an argument,
// they never call time.Now and do no I/O. The engine and scheduler are the
// only clock users, which keeps the rules below table-testable.
//
// Cadence rules (LIFE-05):
//   - An active PR polls every min interval, 60s by default (D-09).
//   - A quiet PR ramps 1m, 1.5m, 2m, 3m, 4m, 6m, 8m, 11m and reaches the
//     16m ceiling after about 4h of quiet, about 57 polls in the first 4h
//     (D-10, D-11). The step thresholds and multipliers scale with min, so a
//     test can run the whole curve at 100ms.
//   - The curve is a pure function of persisted timestamps
//     (LastPollAt - RateAnchor), so a restart resumes the same position and no
//     step counter is stored.
//   - Jitter only shortens an interval; nothing but budget stretch can raise
//     an interval above the ceiling, and stretched intervals are capped at
//     maxStretched (D-11, D-06).
//   - RateAnchor moves to now on activity (D-12, classified in 02-09), on the
//     first subscription that creates the record (D-13), and on ResetRate or
//     PollNow (D-15). keep does not move it (D-14).
//   - Each PR is polled once no matter how many agents subscribe (DMN-02).
//     Near-due PRs within CoalesceWindow are pulled into the same batch.
//
// Budget rules (DMN-04):
//   - pr-relay records its own spend per request in a 60-minute ledger,
//     including forced polls (D-15), projects its hourly spend and stretches
//     every interval so the projection stays within CapPct (10%) of the hourly
//     limit (D-05).
//   - When Remaining falls below LowThreshold (20%) of the limit, every
//     interval stretches in proportion, but polling never stops; at Remaining 0
//     the gate waits until ResetAt (D-06).
//   - A secondary limit waits Retry-After exactly (60s if absent), doubles on
//     each consecutive hit up to 15m (a longer Retry-After is honoured), halves
//     the batch size on each hit and restores it after clean batches (D-07).
//   - Forced polls bypass the own-spend cap gate but never the secondary or
//     primary-exhausted gates, which GitHub enforces (D-15).
package poller
