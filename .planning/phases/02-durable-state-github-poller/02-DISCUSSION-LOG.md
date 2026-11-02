# Phase 2: Durable State & GitHub Poller - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-11-01
**Phase:** 02-durable-state-github-poller
**Areas discussed:** Rate-limit budget share, Polling cadence curve, Pending events & re-subscribe, Headless harness

---

## Rate-limit budget share

| Question | Options | Selected |
|---|---|---|
| Budget cap | Soft cap 10% / Soft cap 25% / No cap | Soft cap 10% |
| Low budget | Stretch then pause near zero / Stretch only, never pause / Pause below threshold | Stretch only, never pause |
| Configurable | Yes with defaults / Fixed in code / You decide | Yes with defaults |
| Secondary limit | Retry-After then exponential / Just Retry-After / You decide | Retry-After then exponential |

## Polling cadence curve

| Question | Options | Selected |
|---|---|---|
| CI pending rate | 30 s / 60 s / 15 s | Free text: 90 s. Later revised to 60 s for every active PR |
| Decay | Doubling ladder to 60m / Time tiers / You decide | Free text: "reach 16 over 4 hours and stay there for 24 hours and then re-ask the agent" |
| Activity | Any snapshot change / Only delivered events / Delivered + own pushes | Delivered + own pushes |
| On sub | Start fast / Keep current rate | Start fast. Later narrowed to the first subscriber only (see the next area) |
| Floor rate | 90 s / 60 s / 2 min | Free text: "start at 60 s" |
| Ramp shape | Geometric / Linear / You decide | Geometric |
| After 24h no keep | 60 min / stay 16 min / 30 min | Free text: keep 16 min, only new activity resets |
| On keep | 16 min + clock reset / Back to fast | 16 min + clock reset |
| CI vs ramp | 60 s everywhere active / CI 90 s, other 60 s / CI pins 60 s | 60 s everywhere active |

## Pending events & re-subscribe

| Question | Options | Selected |
|---|---|---|
| Re-sub | Update in place, keep pending / Fresh start / Update mask, drop pending | Update in place, keep pending |
| Pending TTL | No TTL + cap / 24 h TTL + cap / You decide | No TTL + cap per subscription |
| Corrupt state | Back up, start empty / Refuse to start / Rolling .bak | Back up, start empty, log loudly |
| Second-subscriber baseline | Fresh fetch / Reuse memory / You decide | Free text: reuse the existing polling, tell the agent the current interval, and let the agent force an immediate poll or rate reset |
| Multi-sub | Allow + report co-subscribers / One subscriber per PR / Allow + warn | Allow + report co-subscribers. The user had raised one-subscriber-per-PR; Claude recommended against it, since polling is already shared and relaying between agents costs tokens and is fragile |
| History cut | Timestamp filter / Don't filter | Don't filter |
| Force form | `sub --now` / `pr-relay poll` | Free text: `sub` returns the current rate, then a second command resets that subscription's rate |
| Force guard | 30 s / 60 s / No throttle | No throttle |

## Headless harness

| Question | Options | Selected |
|---|---|---|
| Harness | Hidden dev command / Tests only / Tests + opt-in live | Hidden dev command |
| Crash test | Subprocess kill loop / Fault injection / Both | Subprocess kill loop |

## Claude's Discretion
- State and config locations, the schema version, token-loss behaviour, jitter and batch grouping, how closed PRs stop being polled, following up a Gap, and the exact ramp steps.

## Deferred Ideas
- The force-poll CLI command, to be added as a requirement for Phase 4.
- Doc updates in REQUIREMENTS.md and ROADMAP.md for the 16-min ceiling.
