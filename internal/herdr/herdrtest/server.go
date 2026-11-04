// Package herdrtest is an in-process fake herdr (protocol 22) for tests. It
// reproduces the wire quirks verified against herdr 0.9.5 (events.subscribe
// is described in subscribe.go):
//
//   - one response per connection, then close (a second request on the same
//     connection is never answered)
//   - a non-string id is answered with "id":"" and invalid_request
//   - agent.get, agent.wait and agent.prompt on a missing pane or a bare
//     shell answer agent_not_found, never pane_not_found
//   - pane.get on a missing pane answers pane_not_found; on a bare shell it
//     answers pane_info with agent_status "unknown" and no agent
//   - agent.prompt on a blocked agent answers agent_blocked and records
//     nothing
//   - agent.wait without "until" returns immediately while blocked (the S2a
//     busy-loop trap), honours timeout_ms with code timeout, blocks forever
//     without timeout_ms, and answers agent_not_running when the pinned
//     occupant goes away
//
// # synctest
//
// The fake is synctest-safe only through Dialer(), which serves each
// connection over net.Pipe. Real unix sockets (ListenUnix) are not durably
// blocking, so use them only outside synctest bubbles. Create the Server
// inside the bubble that uses it.
package herdrtest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/herdr"
)

// PaneState is the scripted state of one fake pane. An empty Agent is a bare
// shell; an empty Session means agent_session is null.
type PaneState struct {
	ID            string
	TabID         string
	WorkspaceID   string
	Agent         string
	Session       string
	Status        string
	StateSeq      uint64
	CompletionSeq *uint64
	LaunchPending bool
	// NotReady makes herdr report interactive_ready=false. A ready pane
	// carries no interactive_ready key, as herdr 0.9.5 does for
	// long-running agents.
	NotReady   bool
	TerminalID string
}

// Prompt is one recorded, successful agent.prompt.
type Prompt struct {
	Target string
	Text   string
	Wait   *herdr.WaitOpts
}

// Request is one received request line (method and raw params).
type Request struct {
	Method string
	Params json.RawMessage
}

// pane is a PaneState plus the occupant generation that agent.wait pins.
type pane struct {
	PaneState
	gen uint64
}

// serverConn is one accepted connection, as seen by the dispatch code.
type serverConn struct {
	conn net.Conn
	br   *bufio.Reader
	// gone is closed when the client side hits EOF or a read error.
	gone chan struct{}
	// extra is closed as soon as the client sends any byte after the first
	// request line (herdr closes a subscribe connection on any write).
	extra chan struct{}
}

// subscribeHandler serves events.subscribe. Plan 03-05 sets it from a
// separate file (subscribe.go). It owns sc.conn and must close it. When nil,
// events.subscribe answers invalid_request.
var subscribeHandler func(s *Server, sc *serverConn, id string, params json.RawMessage)

// statusHook, when set, is called with s.mu held after any status or
// occupant change of a pane (plan 03-05 broadcasts
// pane.agent_status_changed from it).
var statusHook func(s *Server, p PaneState)

// Server is a fake herdr. All methods are safe for concurrent use.
type Server struct {
	tb testing.TB

	mu       sync.Mutex
	panes    map[string]*pane
	changed  chan struct{} // closed and replaced on every state change
	failNext map[string][]string
	prompts  []Prompt
	requests []Request
	conns    map[net.Conn]struct{}
	dials    int
	stopped  bool
	ln       net.Listener
	sockPath string
	sockDir  string

	// events.subscribe state (subscribe.go).
	subs         map[*subscriber]struct{}
	subscribes   int
	lastSub      []map[string]any
	closeOnWrite int
	protocol     int
}

// New returns an empty fake herdr. Its listener (if any) and connections
// are closed in tb.Cleanup.
func New(tb testing.TB) *Server {
	s := &Server{
		tb:       tb,
		panes:    map[string]*pane{},
		changed:  make(chan struct{}),
		failNext: map[string][]string{},
		conns:    map[net.Conn]struct{}{},
		subs:     map[*subscriber]struct{}{},
		protocol: herdr.Protocol,
	}
	tb.Cleanup(func() {
		s.Stop()
		s.mu.Lock()
		dir := s.sockDir
		s.mu.Unlock()
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	})
	return s
}

// broadcastLocked wakes every agent.wait. s.mu must be held.
func (s *Server) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Server) statusChangedLocked(p *pane) {
	if statusHook != nil {
		statusHook(s, p.PaneState)
	}
	s.broadcastLocked()
}

// SetPane adds or replaces a pane. Replacing it with a different agent or
// session counts as an occupant change for pinned waits.
func (s *Server) SetPane(ps PaneState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.panes[ps.ID]
	p := &pane{PaneState: ps}
	if old != nil {
		p.gen = old.gen
		if old.Agent != ps.Agent || old.Session != ps.Session {
			p.gen++
		}
	}
	s.panes[ps.ID] = p
	s.statusChangedLocked(p)
}

// RemovePane deletes a pane. Pending waits on it answer agent_not_running.
func (s *Server) RemovePane(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.panes, id)
	s.broadcastLocked()
}

// Pane returns the current scripted state of a pane.
func (s *Server) Pane(id string) (PaneState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.panes[id]
	if !ok {
		return PaneState{}, false
	}
	return p.PaneState, true
}

// SetStatus changes a pane's agent_status, bumps its state_change_seq and
// wakes waiters.
func (s *Server) SetStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.panes[id]
	if !ok {
		s.tb.Errorf("herdrtest: SetStatus: no pane %q", id)
		return
	}
	p.Status = status
	p.StateSeq++
	s.statusChangedLocked(p)
}

// SetOccupant replaces the agent kind and session of a pane (agent "" makes
// it a bare shell). It bumps state_change_seq, and waits pinned to the old
// occupant answer agent_not_running.
func (s *Server) SetOccupant(id, agent, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.panes[id]
	if !ok {
		s.tb.Errorf("herdrtest: SetOccupant: no pane %q", id)
		return
	}
	p.Agent = agent
	p.Session = session
	p.gen++
	p.StateSeq++
	s.statusChangedLocked(p)
}

// FailNext makes the next request for method answer with error code. Calls
// queue up in order.
func (s *Server) FailNext(method, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method] = append(s.failNext[method], code)
}

// Prompts returns every successful agent.prompt, in order.
func (s *Server) Prompts() []Prompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.prompts)
}

// Requests returns every received request (method and raw params), in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Calls returns the method of every received request, in order.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requests))
	for i, r := range s.requests {
		out[i] = r.Method
	}
	return out
}

// CallCount returns how many requests for method were received.
func (s *Server) CallCount(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Method == method {
			n++
		}
	}
	return n
}

// Conns returns how many connections were accepted (pipe or unix).
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dials
}

func refused(socket string) error {
	return &net.OpError{Op: "dial", Net: "unix", Addr: &net.UnixAddr{Name: socket, Net: "unix"},
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
}

// Dialer returns a herdr.DialFunc that serves each connection over
// net.Pipe, ignoring the socket path. After Stop it fails with an error
// wrapping syscall.ECONNREFUSED until Restart.
func (s *Server) Dialer() herdr.DialFunc {
	return func(ctx context.Context, socket string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return nil, refused(socket)
		}
		client, server := net.Pipe()
		s.conns[server] = struct{}{}
		s.dials++
		s.mu.Unlock()
		go s.serve(server)
		return client, nil
	}
}

// ListenUnix serves on a real unix socket at a short temporary path (macOS
// limits sun_path to 104 bytes) and returns the path. The socket is closed
// and removed in tb.Cleanup.
func (s *Server) ListenUnix() string {
	s.tb.Helper()
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		s.tb.Fatalf("herdrtest: MkdirTemp: %v", err)
	}
	path := filepath.Join(dir, "h.sock")
	return s.listenAt(dir, path)
}

// ListenUnixAt serves on a real unix socket at path (its directory must
// exist). The socket is closed and removed in tb.Cleanup.
func (s *Server) ListenUnixAt(path string) string {
	s.tb.Helper()
	return s.listenAt("", path)
}

func (s *Server) listenAt(dir, path string) string {
	s.tb.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		s.tb.Fatalf("herdrtest: listen %s: %v", path, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.sockPath = path
	if dir != "" {
		s.sockDir = dir
	}
	s.stopped = false
	s.mu.Unlock()
	go s.accept(ln)
	return path
}

func (s *Server) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			_ = c.Close()
			continue
		}
		s.conns[c] = struct{}{}
		s.dials++
		s.mu.Unlock()
		go s.serve(c)
	}
}

// Stop simulates herdr going away: it closes every open connection and the
// listener, removes the socket file, and makes Dialer refuse connections.
func (s *Server) Stop() {
	s.mu.Lock()
	s.stopped = true
	ln := s.ln
	s.ln = nil
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	path := s.sockPath
	s.broadcastLocked()
	s.dropSubscribersLocked()
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
}

// Restart undoes Stop: it re-listens on the same unix path (when ListenUnix
// was used) and re-enables Dialer. Scripted pane state is kept.
func (s *Server) Restart() {
	s.tb.Helper()
	s.mu.Lock()
	path := s.sockPath
	s.stopped = false
	s.mu.Unlock()
	if path != "" {
		s.listenAt("", path)
	}
}

func (s *Server) dropConn(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	_ = c.Close()
}

// wireRequest is a request line. ID stays raw so a non-string id can be
// detected.
type wireRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// wireError is an error reply (nil means "send nothing", e.g. after Stop).
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func errReply(code, msg string) *wireError { return &wireError{Code: code, Message: msg} }

// serve reads exactly one request line, answers exactly one line and closes.
func (s *Server) serve(c net.Conn) {
	sc := &serverConn{conn: c, br: bufio.NewReader(c), gone: make(chan struct{}), extra: make(chan struct{})}
	line, err := sc.br.ReadBytes('\n')
	if err != nil {
		s.dropConn(c)
		return
	}
	go sc.drain()

	var req wireRequest
	var id string
	if err := json.Unmarshal(line, &req); err != nil || json.Unmarshal(req.ID, &id) != nil {
		s.reply(sc, "", nil, errReply(herdr.CodeInvalidRequest, "invalid request"))
		s.dropConn(c)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: req.Method, Params: slices.Clone(req.Params)})
	var scripted string
	if q := s.failNext[req.Method]; len(q) > 0 {
		scripted = q[0]
		s.failNext[req.Method] = q[1:]
	}
	s.mu.Unlock()

	if scripted != "" {
		s.reply(sc, id, nil, errReply(scripted, "scripted "+scripted))
		s.dropConn(c)
		return
	}
	if req.Method == "events.subscribe" && subscribeHandler != nil {
		subscribeHandler(s, sc, id, req.Params)
		return
	}
	result, werr, send := s.dispatch(sc, req.Method, req.Params)
	if send {
		s.reply(sc, id, result, werr)
	}
	s.dropConn(c)
}

// drain consumes anything the client writes after the first line, so a
// second pipelined request is read and ignored (never answered). It closes
// extra on the first extra byte and gone on EOF or a read error.
func (sc *serverConn) drain() {
	extraClosed := false
	markExtra := func() {
		if !extraClosed {
			extraClosed = true
			close(sc.extra)
		}
	}
	if sc.br.Buffered() > 0 {
		markExtra()
	}
	buf := make([]byte, 512)
	for {
		n, err := sc.br.Read(buf)
		if n > 0 {
			markExtra()
		}
		if err != nil {
			close(sc.gone)
			return
		}
	}
}

func (s *Server) reply(sc *serverConn, id string, result any, werr *wireError) {
	msg := map[string]any{"id": id}
	if werr != nil {
		msg["error"] = werr
	} else {
		msg["result"] = result
	}
	b, err := json.Marshal(msg)
	if err != nil {
		s.tb.Errorf("herdrtest: marshal reply: %v", err)
		return
	}
	_, _ = sc.conn.Write(append(b, '\n'))
}

// dispatch answers one method. send is false when no reply must be written
// (the client went away or the server was stopped mid-wait).
func (s *Server) dispatch(sc *serverConn, method string, params json.RawMessage) (result any, werr *wireError, send bool) {
	switch method {
	case "ping":
		return map[string]any{"type": "pong", "version": "0.9.5", "protocol": s.currentProtocol(),
			"capabilities": map[string]any{}}, nil, true
	case "agent.get":
		var p struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, errReply(herdr.CodeInvalidRequest, err.Error()), true
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		pn := s.panes[p.Target]
		if pn == nil || pn.Agent == "" {
			return nil, agentNotFound(p.Target), true
		}
		return map[string]any{"type": "agent_info", "agent": agentJSON(pn)}, nil, true
	case "agent.prompt":
		return s.agentPrompt(params)
	case "agent.wait":
		return s.agentWait(sc, params)
	case "pane.get":
		var p struct {
			PaneID string `json:"pane_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, errReply(herdr.CodeInvalidRequest, err.Error()), true
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		pn := s.panes[p.PaneID]
		if pn == nil {
			return nil, errReply(herdr.CodePaneNotFound, fmt.Sprintf("pane %s not found", p.PaneID)), true
		}
		return map[string]any{"type": "pane_info", "pane": paneJSON(pn)}, nil, true
	case "pane.list":
		s.mu.Lock()
		defer s.mu.Unlock()
		ids := make([]string, 0, len(s.panes))
		for id := range s.panes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		panes := make([]any, 0, len(ids))
		for _, id := range ids {
			panes = append(panes, paneJSON(s.panes[id]))
		}
		return map[string]any{"type": "pane_list", "panes": panes}, nil, true
	default:
		return nil, errReply(herdr.CodeInvalidRequest, "unknown method "+method), true
	}
}

func agentNotFound(target string) *wireError {
	return errReply(herdr.CodeAgentNotFound, fmt.Sprintf("no agent found in pane %s", target))
}

func (s *Server) agentPrompt(params json.RawMessage) (any, *wireError, bool) {
	var p struct {
		Target string          `json:"target"`
		Text   string          `json:"text"`
		Wait   *herdr.WaitOpts `json:"wait"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errReply(herdr.CodeInvalidRequest, err.Error()), true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pn := s.panes[p.Target]
	if pn == nil || pn.Agent == "" {
		return nil, agentNotFound(p.Target), true
	}
	if pn.Status == herdr.StatusBlocked {
		return nil, errReply(herdr.CodeAgentBlocked, "agent is blocked; prompt not sent"), true
	}
	rec := Prompt{Target: p.Target, Text: p.Text}
	if p.Wait != nil {
		w := *p.Wait
		w.Until = slices.Clone(w.Until)
		rec.Wait = &w
	}
	s.prompts = append(s.prompts, rec)
	if p.Wait != nil && slices.Contains(p.Wait.Until, herdr.StatusWorking) && pn.Status != herdr.StatusWorking {
		pn.Status = herdr.StatusWorking
		pn.StateSeq++
		s.statusChangedLocked(pn)
	}
	return map[string]any{"type": "agent_prompted", "agent": agentJSON(pn)}, nil, true
}

func (s *Server) agentWait(sc *serverConn, params json.RawMessage) (any, *wireError, bool) {
	var p struct {
		Target    string   `json:"target"`
		Until     []string `json:"until"`
		TimeoutMs *int64   `json:"timeout_ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errReply(herdr.CodeInvalidRequest, err.Error()), true
	}
	s.mu.Lock()
	pn := s.panes[p.Target]
	if pn == nil || pn.Agent == "" {
		s.mu.Unlock()
		return nil, agentNotFound(p.Target), true
	}
	gen, startSeq := pn.gen, pn.StateSeq
	var timeout <-chan time.Time
	if p.TimeoutMs != nil {
		t := time.NewTimer(time.Duration(*p.TimeoutMs) * time.Millisecond)
		defer t.Stop()
		timeout = t.C
	}
	for {
		pn = s.panes[p.Target]
		if s.stopped {
			s.mu.Unlock()
			return nil, nil, false
		}
		if pn == nil || pn.gen != gen || pn.Agent == "" {
			s.mu.Unlock()
			return nil, errReply(herdr.CodeAgentNotRunning, "agent is no longer running in the target pane"), true
		}
		var ok bool
		if len(p.Until) == 0 {
			// S2a: without until, a blocked agent satisfies the wait at once.
			ok = pn.Status == herdr.StatusBlocked || pn.StateSeq != startSeq
		} else {
			ok = slices.Contains(p.Until, pn.Status)
		}
		if ok {
			res := map[string]any{"type": "agent_info", "agent": agentJSON(pn)}
			s.mu.Unlock()
			return res, nil, true
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-timeout:
			return nil, errReply(herdr.CodeTimeout, "timed out waiting for agent status"), true
		case <-sc.gone:
			return nil, nil, false
		}
		s.mu.Lock()
	}
}

func sessionJSON(ps PaneState) any {
	if ps.Session == "" {
		return nil
	}
	return map[string]any{"source": "herdrtest", "agent": ps.Agent, "kind": "session_id", "value": ps.Session}
}

func status(ps PaneState) string {
	if ps.Agent == "" || ps.Status == "" {
		return herdr.StatusUnknown
	}
	return ps.Status
}

// agentJSON renders AgentInfo with the wire's snake_case keys, written out
// by hand so the herdr package's json tags are checked against it.
func agentJSON(p *pane) map[string]any {
	var completion any
	if p.CompletionSeq != nil {
		completion = *p.CompletionSeq
	}
	m := map[string]any{
		"pane_id":          p.ID,
		"tab_id":           p.TabID,
		"workspace_id":     p.WorkspaceID,
		"agent":            p.Agent,
		"agent_session":    sessionJSON(p.PaneState),
		"agent_status":     status(p.PaneState),
		"state_change_seq": p.StateSeq,
		"completion_seq":   completion,
		"terminal_id":      p.TerminalID,
		"focused":          false,
		"launch_pending":   p.LaunchPending,
	}
	// herdr 0.9.5 omits interactive_ready for ready long-running agents;
	// only an explicit false means not ready.
	if p.LaunchPending || p.NotReady {
		m["interactive_ready"] = false
	}
	return m
}

func paneJSON(p *pane) map[string]any {
	m := map[string]any{
		"pane_id":       p.ID,
		"tab_id":        p.TabID,
		"workspace_id":  p.WorkspaceID,
		"agent_status":  status(p.PaneState),
		"terminal_id":   p.TerminalID,
		"agent_session": sessionJSON(p.PaneState),
		"focused":       false,
	}
	if p.Agent != "" {
		m["agent"] = p.Agent
	}
	return m
}
