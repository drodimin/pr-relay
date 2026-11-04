// Package deliver pushes pending PR updates into herdr panes.
//
// One worker runs per (herdr socket, pane) that has pending updates (or a
// drop marker) in any subscription, and exits once the pane has nothing
// pending. Each worker:
//
//   - gates on the agent's status: it types only when herdr reports idle or
//     done (the same class) and nothing changed for the quiet period (D-08,
//     D-09). Working and blocked agents are waited on with agent.wait and an
//     explicit until list and timeout (D-03); idle agents are never waited
//     on, a timer is used instead (no hot loop). Unknown status, a launch
//     still pending, an explicit interactive_ready=false and a pane with no
//     agent are never typed into (DLV-03). An absent interactive_ready key
//     (herdr 0.9.5 on long-running agents) counts as ready.
//   - runs the D-13 identity check before every prompt, and only on a
//     settled snapshot (no launch pending, known status, readiness not
//     explicitly false): an unsettled snapshot never cancels. A stored
//     session must equal the current one; without a stored session the
//     agent kind must match. A mismatch cancels that subscription only; an
//     agent whose session is not identified yet is held.
//   - composes every pending update of every subscription of the pane into
//     one message rendered from each PR's current state (DLV-04, DLV-07) and
//     sends it with one agent.prompt. An update whose PR cannot be rendered
//     stays queued.
//   - acks only after agent.prompt succeeded (herdr observed the agent
//     working), or after a later agent.get shows completion_seq advanced
//     past its pre-prompt value. A wait-for-working timeout is not delivery:
//     a just-relaunched agent can report idle and drop the text (SC3). The
//     update stays queued and is re-sent with exponential backoff from the
//     recheck interval, capped at 16 minutes. A duplicate is accepted
//     (D-05), a loss is not. Every other outcome keeps the updates queued
//     (D-04).
//   - cancels the pane's subscriptions when herdr reports the pane gone at
//     push time, once herdr has been up for a restore grace (LIFE-04, D-15).
//     Cancellations are logged only; nothing is typed into a pane about them
//     (D-17).
//   - reports to the Engine when the pane cannot receive and when it can
//     again (LIFE-08).
//
// The pane's focused flag and terminal_id are never used.
package deliver
