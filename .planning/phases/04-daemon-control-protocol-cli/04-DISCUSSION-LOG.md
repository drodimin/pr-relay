# Phase 4: Daemon, Control Protocol & CLI - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-11-04
**Phase:** 04-daemon-control-protocol-cli
**Areas discussed:** sub UX & validation, Check-in & expiry, Daemon lifecycle, ls / status / unsub scope

---

## sub UX & validation

| Question | Options | Selected |
|----------|---------|----------|
| sub success output | Terse text 1-3 lines / Text + --json / JSON always | Terse text ✓ |
| Validation when GitHub slow/limited | Bounded wait then fail / Bounded wait then accept provisionally / Syntax check only | Bounded wait then fail ✓ |
| Force-poll form | `pr-relay poll <PR>` / `sub --now` / Both | `poll` ✓ |
| sub in pane with no agent | Allow with warning / Reject | Allow with warning ✓ |

## Check-in & expiry

| Question | Options | Selected |
|----------|---------|----------|
| What resets the clock | Delivered events for that sub / Any PR activity / Only keep/sub | Delivered events for that sub ✓ |
| Message wording | Two-line command-forward / Minimal | Command-forward ✓ |
| Ignored check-in | One only / Repeat every 24h | Repeat every 24h ✓ (overrides LIFE-06 "one") |
| Undeliverable expiry | Remove at 7d, queue message / Remove only after delivery | Remove at 7d, queue ✓ |
| closed vs mask / reopen | Always deliver closed, remove after / Respect mask, remove silently / Remove on merge only | Always deliver closed ✓ |

## Daemon lifecycle

| Question | Options | Selected |
|----------|---------|----------|
| Version mismatch | Auto-restart transparently / Restart only if CLI newer / Clear error only | Auto-restart ✓ |
| Idle exit | Never / Exit after N min with zero subs | Never ✓ |
| Daemon commands | `daemon` + `stop`/`restart` / `daemon` only | `daemon` + `stop`/`restart` ✓ |
| dev harness | Keep hidden / Remove | Keep hidden ✓ |
| P3 IN-01..IN-03 | Fold all / Only IN-03 / Leave deferred | Fold all ✓ |

## ls / status / unsub scope

| Question | Options | Selected |
|----------|---------|----------|
| ls scope | Calling pane, --all for everything / Everything always | Calling pane + --all ✓ |
| --json | Yes on ls+status / No, text only | No, text only ✓ |
| status extras (multi) | Recent cancellations / Auth & corrupt-state / herdr per socket / Paths & version | Cancellations, herdr per socket, Paths & version ✓ |
| Drop everything machine-wide | `unsub --all` outside herdr / `unsub --everywhere` / none | Free text: "reset" → dedicated `pr-relay reset` |
| unsub with no match | Success + note / Error | Success + note ✓ |

**Notes:** The user asked "how would I drop all the subscriptions?" because none of the planned commands did it. The answer was a dedicated `reset` command.

## Claude's Discretion

- The `reset` confirmation: a TTY prompt, or `--yes` when not on a TTY.
- `--events` syntax and aliases, which URL forms are accepted, the control-protocol schema and version handshake, where identity is captured, log rotation, auto-start backoff, the `ls` layout, and how lifecycle messages map onto the outbox.
- Auth and corrupt-state warnings in `status`: these fields already exist and are included.

## Deferred Ideas

- `--json` for `ls` and `status`.
- REQUIREMENTS.md doc updates (LIFE-06 repeat, 16-min ceiling, SUB-10 `poll`, `reset`).
