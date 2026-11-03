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
//     still pending and a pane with no agent are never typed into (DLV-03).
//   - runs the D-13 identity check before every prompt: a stored session
//     must equal the current one; without a stored session the agent kind
//     must match. A mismatch cancels that subscription only; an agent whose
//     session is not identified yet is held.
//   - composes every pending update of every subscription of the pane into
//     one message rendered from each PR's current state (DLV-04, DLV-07) and
//     sends it with one agent.prompt.
//   - acks only after the prompt succeeded, or after its working-wait timed
//     out once the text was submitted (D-05, at-least-once). Every other
//     outcome keeps the updates queued (D-04).
//   - cancels the pane's subscriptions when herdr reports the pane gone at
//     push time, once herdr has been up for a restore grace (LIFE-04, D-15).
//     Cancellations are logged only; nothing is typed into a pane about them
//     (D-17).
//   - reports to the Engine when the pane cannot receive and when it can
//     again (LIFE-08).
//
// The pane's focused flag and terminal_id are never used.
package deliver
