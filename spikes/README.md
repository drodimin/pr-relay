# pr-relay spikes (Phase 1, plan 01-08)

Throwaway probes that answer the runtime questions S0-S7 recorded in
`.planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md`. They are not
part of the product.

## Safety rules (D-16)

- **prspike only.** `lib.sh` hard-codes the socket
  `~/.config/herdr/sessions/prspike/herdr.sock`, overrides any inherited
  `HERDR_SOCKET_PATH`, and unsets `HERDR_SESSION` and `HERDR_PANE_ID`. Every
  herdr call goes through the `hr` wrapper, which runs `guard_socket` first
  (exit 97 if the socket is not a prspike socket or does not exist).
  `guard_target` also requires the pane to appear in prspike's agent list
  (exit 98). Pane ids such as `w1:p1` exist in every session, so the socket
  path is the real protection.
- **Comment payloads.** Every prompt starts with `# pr-relay spike:` (or `# `
  on every line for multi-line payloads), so text that lands in a shell is a
  harmless comment.
- **Run from a plain terminal.** `run-interactive.sh` refuses to run when
  `HERDR_PANE_ID` or `HERDR_SESSION` is set, that is, inside any herdr pane.
- **The scripts never stop or delete a session.** Steps S3 and S5 ask you to
  run `herdr session stop prspike` yourself.
- Raw logs go to `spikes/out/` (gitignored). Only redacted excerpts are copied
  into 01-SPIKES.md.

## Order

1. `spikes/s4-graphql-cost.sh` (no herdr, needs only `gh`; already run by
   the executor). Set `PR_RELAY_FIXTURE_REPO=owner/name` to target the dummy
   repo instead of `cli/cli`.
2. In a terminal that is not inside herdr: `herdr --session prspike` (or
   `herdr session attach prspike` if it already exists). Make pane A a scratch
   agent in an empty temp dir and pane B a bare shell.
3. In a second plain terminal: `bash spikes/run-interactive.sh` and follow the
   prompts (S0, S1, S7, S2a, S2b, S6, S3, S5).
4. Optional cleanup: `herdr session stop prspike && herdr session delete prspike`.

`setsid-probe/` is a stdlib-only Go program for S5. Build it with
`go build -o spikes/setsid-probe/setsid-probe ./spikes/setsid-probe` (not
`go run`). Its child writes heartbeats to `spikes/out/setsid-heartbeat.txt`
and exits by itself after 15 minutes.
