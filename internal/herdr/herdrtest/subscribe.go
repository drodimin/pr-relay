package herdrtest

// events.subscribe support. Verified quirks reproduced here (herdr 0.9.5,
// protocol 22):
//
//   - the ack is {"id":<id>,"result":{"type":"subscription_started"}},
//     then event lines with no id
//   - lifecycle events use underscore names and repeat them in data.type
//     (pane_closed, pane_moved, pane_agent_detected, tab_closed, ...)
//   - per-pane status events use the dotted name pane.agent_status_changed
//     and carry no data.type
//   - pane.agent_status_changed without pane_id answers invalid_request,
//     and any listed pane_id that does not exist answers pane_not_found;
//     both close the connection
//   - writing anything on an open subscribe connection makes herdr close it
//   - events_lost is an error line carrying the original subscribe id,
//     followed by close
//
// Event lines are queued per subscriber and written by a dedicated
// goroutine, so emitting never blocks on a slow reader while s.mu is held.

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/drodimin/pr-relay/internal/herdr"
)

func init() {
	subscribeHandler = handleSubscribe
	statusHook = broadcastStatus
}

// knownSubTypes are the subscription request types the fake accepts.
var knownSubTypes = map[string]bool{
	"pane.closed":               true,
	"pane.moved":                true,
	"pane.agent_detected":       true,
	"pane.exited":               true,
	"tab.closed":                true,
	"workspace.closed":          true,
	"pane.agent_status_changed": true,
}

type outLine struct {
	line  []byte
	close bool // close the connection after writing this line
}

// subscriber is one open events.subscribe connection.
type subscriber struct {
	s     *Server
	sc    *serverConn
	id    string
	types map[string]bool
	panes map[string]bool // panes listed for pane.agent_status_changed

	qmu    sync.Mutex
	queue  []outLine
	notify chan struct{} // cap 1
	done   chan struct{}
	once   sync.Once
}

func (sub *subscriber) enqueue(l outLine) {
	sub.qmu.Lock()
	sub.queue = append(sub.queue, l)
	sub.qmu.Unlock()
	select {
	case sub.notify <- struct{}{}:
	default:
	}
}

// shutdown closes the connection once and forgets the subscriber.
func (sub *subscriber) shutdown() {
	sub.once.Do(func() {
		close(sub.done)
		sub.s.mu.Lock()
		delete(sub.s.subs, sub)
		sub.s.mu.Unlock()
		sub.s.dropConn(sub.sc.conn)
	})
}

func (sub *subscriber) writer() {
	for {
		select {
		case <-sub.notify:
		case <-sub.done:
			return
		}
		sub.qmu.Lock()
		q := sub.queue
		sub.queue = nil
		sub.qmu.Unlock()
		for _, l := range q {
			if _, err := sub.sc.conn.Write(l.line); err != nil || l.close {
				sub.shutdown()
				return
			}
		}
	}
}

// watch closes the connection as soon as the client writes anything
// (verified herdr behaviour) or goes away.
func (sub *subscriber) watch() {
	select {
	case <-sub.sc.extra:
		sub.s.mu.Lock()
		sub.s.closeOnWrite++
		sub.s.mu.Unlock()
	case <-sub.sc.gone:
	case <-sub.done:
	}
	sub.shutdown()
}

func handleSubscribe(s *Server, sc *serverConn, id string, params json.RawMessage) {
	var p struct {
		Subscriptions []struct {
			Type   string  `json:"type"`
			PaneID *string `json:"pane_id"`
		} `json:"subscriptions"`
	}
	fail := func(code, msg string) {
		s.reply(sc, id, nil, errReply(code, msg))
		s.dropConn(sc.conn)
	}
	if err := json.Unmarshal(params, &p); err != nil {
		fail(herdr.CodeInvalidRequest, err.Error())
		return
	}
	sub := &subscriber{s: s, sc: sc, id: id, types: map[string]bool{}, panes: map[string]bool{},
		notify: make(chan struct{}, 1), done: make(chan struct{})}
	record := make([]map[string]any, 0, len(p.Subscriptions))

	s.mu.Lock()
	for _, e := range p.Subscriptions {
		if !knownSubTypes[e.Type] {
			s.mu.Unlock()
			fail(herdr.CodeInvalidRequest, fmt.Sprintf("unknown variant `%s`", e.Type))
			return
		}
		m := map[string]any{"type": e.Type}
		if e.Type == "pane.agent_status_changed" {
			if e.PaneID == nil {
				s.mu.Unlock()
				fail(herdr.CodeInvalidRequest, "missing field `pane_id`")
				return
			}
			if s.panes[*e.PaneID] == nil {
				s.mu.Unlock()
				fail(herdr.CodePaneNotFound, fmt.Sprintf("pane %s not found", *e.PaneID))
				return
			}
			sub.panes[*e.PaneID] = true
			m["pane_id"] = *e.PaneID
		}
		sub.types[e.Type] = true
		record = append(record, m)
	}
	if s.stopped {
		s.mu.Unlock()
		s.dropConn(sc.conn)
		return
	}
	// Register before the ack, so events emitted once the client has seen
	// subscription_started are never missed. They queue until the writer
	// starts, which keeps the ack first on the wire.
	s.subs[sub] = struct{}{}
	s.subscribes++
	s.lastSub = record
	s.mu.Unlock()

	s.reply(sc, id, map[string]any{"type": "subscription_started"}, nil)
	go sub.writer()
	go sub.watch()
}

// dropSubscribersLocked forgets every subscriber (their connections are
// closed by the caller, e.g. Stop). s.mu must be held.
func (s *Server) dropSubscribersLocked() {
	for sub := range s.subs {
		delete(s.subs, sub)
	}
}

func (s *Server) currentProtocol() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protocol
}

func eventLine(event string, data map[string]any) []byte {
	b, err := json.Marshal(map[string]any{"event": event, "data": data})
	if err != nil {
		panic(err) // maps of strings and nested maps always marshal
	}
	return append(b, '\n')
}

// emitLocked queues line to every subscriber that subscribed to subType.
// s.mu must be held.
func (s *Server) emitLocked(subType string, line []byte) {
	for sub := range s.subs {
		if sub.types[subType] {
			sub.enqueue(outLine{line: line})
		}
	}
}

// broadcastStatus is statusHook: it sends the dotted
// pane.agent_status_changed line to subscribers that listed the pane.
func broadcastStatus(s *Server, p PaneState) {
	line := eventLine("pane.agent_status_changed", map[string]any{
		"agent":        p.Agent,
		"agent_status": status(p),
		"pane_id":      p.ID,
		"workspace_id": p.WorkspaceID,
	})
	for sub := range s.subs {
		if sub.types["pane.agent_status_changed"] && sub.panes[p.ID] {
			sub.enqueue(outLine{line: line})
		}
	}
}

// SetProtocol changes the protocol number ping reports (default
// herdr.Protocol).
func (s *Server) SetProtocol(p int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.protocol = p
}

// EmitRaw writes line (a newline is appended) to every open subscriber,
// whatever it subscribed to.
func (s *Server) EmitRaw(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		sub.enqueue(outLine{line: []byte(line + "\n")})
	}
}

// ClosePane removes the pane and emits the underscore pane_closed line to
// subscribers of pane.closed. Pending waits on it answer agent_not_running.
func (s *Server) ClosePane(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.panes[id]
	if p == nil {
		s.tb.Errorf("herdrtest: ClosePane: no pane %q", id)
		return
	}
	delete(s.panes, id)
	s.broadcastLocked()
	s.emitLocked("pane.closed", eventLine("pane_closed", map[string]any{
		"type": "pane_closed", "pane_id": id, "workspace_id": p.WorkspaceID,
	}))
}

// MovePane re-keys pane oldID to newID and emits pane_moved (with
// previous_pane_id and the new pane object) to subscribers of pane.moved.
func (s *Server) MovePane(oldID, newID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.panes[oldID]
	if p == nil {
		s.tb.Errorf("herdrtest: MovePane: no pane %q", oldID)
		return
	}
	prevTab, prevWS := p.TabID, p.WorkspaceID
	delete(s.panes, oldID)
	p.ID = newID
	s.panes[newID] = p
	s.broadcastLocked()
	s.emitLocked("pane.moved", eventLine("pane_moved", map[string]any{
		"type":                  "pane_moved",
		"previous_pane_id":      oldID,
		"previous_tab_id":       prevTab,
		"previous_workspace_id": prevWS,
		"pane":                  paneJSON(p),
	}))
}

// DetectAgent sets the pane's occupant (an occupant change for pinned
// waits) and emits pane_agent_detected to subscribers of
// pane.agent_detected. It does not emit a status change.
func (s *Server) DetectAgent(id, kind, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.panes[id]
	if p == nil {
		s.tb.Errorf("herdrtest: DetectAgent: no pane %q", id)
		return
	}
	p.Agent = kind
	p.Session = session
	p.gen++
	p.StateSeq++
	s.broadcastLocked()
	s.emitLocked("pane.agent_detected", eventLine("pane_agent_detected", map[string]any{
		"type": "pane_agent_detected", "pane_id": id, "workspace_id": p.WorkspaceID, "agent": kind,
	}))
}

// CloseTab emits tab_closed only. Whether herdr also emits pane_closed for
// each pane of the tab is unverified (A7), so the fake does not.
func (s *Server) CloseTab(tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked("tab.closed", eventLine("tab_closed", map[string]any{
		"type": "tab_closed", "tab_id": tabID,
	}))
}

// LoseEvents sends events_lost (with each subscriber's original subscribe
// id) to every open subscriber, then closes those connections.
func (s *Server) LoseEvents() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		b, err := json.Marshal(map[string]any{"id": sub.id, "error": map[string]any{
			"code": herdr.CodeEventsLost, "message": "subscriber fell behind; events were dropped",
		}})
		if err != nil {
			panic(err)
		}
		sub.enqueue(outLine{line: append(b, '\n'), close: true})
		delete(s.subs, sub)
	}
}

// Subscribes returns how many events.subscribe requests succeeded.
func (s *Server) Subscribes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribes
}

// Subscribers returns how many subscribe connections are currently open.
func (s *Server) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// LastSubscription returns the subscription list of the last successful
// subscribe, in request order ("type" and, for status entries, "pane_id").
func (s *Server) LastSubscription() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.lastSub))
	for i, m := range s.lastSub {
		c := make(map[string]any, len(m))
		for k, v := range m {
			c[k] = v
		}
		out[i] = c
	}
	return slices.Clip(out)
}

// CloseOnWrites returns how many subscribe connections were closed because
// the client wrote on them.
func (s *Server) CloseOnWrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeOnWrite
}
