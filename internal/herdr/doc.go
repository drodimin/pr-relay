// Package herdr is pr-relay's NDJSON client for the herdr socket API,
// protocol 22 (herdr 0.9.5). It is the only package that talks to herdr
// sockets, and it knows nothing about pull requests: callers address panes
// by (socket, pane id) and pass opaque prompt text.
//
// # Framing
//
// A request is one JSON object on one line:
//
//	{"id":"<string>","method":"<method>","params":{...}}
//
// and herdr answers with one line carrying either "result" (an object whose
// "type" names the payload) or "error" ({"code","message"}).
//
// # One request per connection (verified)
//
// herdr closes every request connection after its first response, so
// pipelining or pooling is impossible. Client.Call dials a fresh connection
// for every request, writes one line, reads one line and closes it. The
// event stream (events.subscribe) uses a separate, long-lived, read-only
// connection (plan 03-05): writing anything on a subscribe connection makes
// herdr close it.
//
// # Errors
//
// A response error decodes into *Error, and Code(err) returns its code
// (agent_not_found, agent_blocked, timeout, ...). Any failure to reach herdr
// at all (dial refused, socket gone, socket not owned by the current user,
// EOF before a reply) is an *UnreachableError instead, reported by
// IsUnreachable. Delivery keeps updates queued while herdr is unreachable
// (DLV-08), so the two must never be confused.
//
// # Bounded waits
//
// agent.wait without timeout_ms blocks forever, and agent.wait without an
// "until" list returns immediately while the agent is blocked (a busy-loop
// trap). Client.AgentWait therefore requires a non-empty until list and
// always sends timeout_ms, with a read deadline of the timeout plus a
// fixed slack. Cancelling the context closes the connection, which unblocks
// any pending read.
package herdr
