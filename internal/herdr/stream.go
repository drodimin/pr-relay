package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// Subscription types accepted by events.subscribe (protocol 22). Request
// types are always dotted; SubAgentStatusChanged requires a PaneID.
const (
	SubPaneClosed         = "pane.closed"
	SubPaneMoved          = "pane.moved"
	SubPaneAgentDetected  = "pane.agent_detected"
	SubPaneExited         = "pane.exited"
	SubTabClosed          = "tab.closed"
	SubWorkspaceClosed    = "workspace.closed"
	SubAgentStatusChanged = "pane.agent_status_changed"
)

// Subscription is one entry of the events.subscribe request.
type Subscription struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id,omitempty"`
}

// EventKind classifies an Event.
type EventKind string

// Event kinds. KindReconnected, KindResubscribed and KindDisconnected are
// synthesised by Endpoint; the rest decode herdr event lines.
const (
	KindPaneClosed         EventKind = "pane_closed"
	KindPaneMoved          EventKind = "pane_moved"
	KindAgentDetected      EventKind = "agent_detected"
	KindPaneExited         EventKind = "pane_exited"
	KindTabClosed          EventKind = "tab_closed"
	KindWorkspaceClosed    EventKind = "workspace_closed"
	KindAgentStatusChanged EventKind = "agent_status_changed"
	// KindReconnected follows every successful connect after the link was
	// down (or lost events). Lifecycle events are not replayed, so
	// consumers re-check their panes.
	KindReconnected EventKind = "reconnected"
	// KindResubscribed is emitted after a pane-set resubscribe (SetPanes or
	// an omitted-pane retry). The link never went down, so UpSince is kept
	// and consumers must not treat it as a reconnect (IN-03).
	KindResubscribed EventKind = "resubscribed"
	// KindDisconnected means the events connection dropped (herdr gone).
	KindDisconnected EventKind = "disconnected"
)

// Event is one decoded herdr event. Kind "" means an event pr-relay does
// not know; callers ignore it. At is filled by Endpoint.
type Event struct {
	Kind EventKind
	// PaneID is the pane concerned; for KindPaneMoved it is the new id.
	PaneID string
	// PreviousPaneID is set for KindPaneMoved.
	PreviousPaneID string
	TabID          string
	WorkspaceID    string
	Agent          string
	AgentStatus    string
	At             time.Time
}

// Stream is a read-only events.subscribe connection. herdr closes a
// subscribe connection on any write, so Stream has no write method.
type Stream struct {
	conn   net.Conn
	br     *bufio.Reader
	socket string
	once   sync.Once
}

// readLine reads one newline-terminated line bounded by maxLine (T-03-17).
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > maxLine {
			return nil, errLineTooLong
		}
		line = append(line, chunk...)
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

var errLineTooLong = fmt.Errorf("herdr: event line exceeds %d bytes", maxLine)

// Subscribe opens an events connection with subs. A herdr error reply
// (pane_not_found, invalid_request) is returned as *Error; a dial, write
// or read failure as *UnreachableError; a cancelled ctx as the ctx error.
// ctx only bounds the handshake: close the Stream to end it.
func (c *Client) Subscribe(ctx context.Context, subs []Subscription) (*Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("herdr: events.subscribe: %w", err)
	}
	if subs == nil {
		subs = []Subscription{}
	}
	id := "sub-" + strconv.FormatUint(c.seq.Add(1), 10)
	b, err := json.Marshal(request{ID: id, Method: "events.subscribe",
		Params: map[string]any{"subscriptions": subs}})
	if err != nil {
		return nil, fmt.Errorf("herdr: encode events.subscribe: %w", err)
	}
	conn, err := c.dial(ctx, c.socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("herdr: events.subscribe: %w", ctx.Err())
		}
		return nil, &UnreachableError{Socket: c.socket, Err: err}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	ok := false
	defer func() {
		stop()
		if !ok {
			_ = conn.Close()
		}
	}()
	fail := func(err error) error {
		if ctx.Err() != nil {
			return fmt.Errorf("herdr: events.subscribe: %w", ctx.Err())
		}
		return &UnreachableError{Socket: c.socket, Err: err}
	}

	dl := time.Now().Add(callTimeout)
	if cd, has := ctx.Deadline(); has && cd.Before(dl) {
		dl = cd
	}
	_ = conn.SetDeadline(dl)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return nil, fail(err)
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(br)
	if err != nil {
		if errors.Is(err, errLineTooLong) {
			return nil, err
		}
		return nil, fail(err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("herdr: decode events.subscribe: %w", err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.ID != id {
		return nil, fmt.Errorf("herdr: decode events.subscribe: response id %q, want %q", resp.ID, id)
	}
	var res struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("herdr: decode events.subscribe result: %w", err)
	}
	if err := checkType("events.subscribe", res.Type, "subscription_started"); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	c.log.Debug("herdr subscribed", "socket", c.socket, "subscriptions", len(subs))
	ok = true
	return &Stream{conn: conn, br: br, socket: c.socket}, nil
}

// streamLine is one line on a subscribe connection: an event (no id) or an
// error line carrying the subscribe id (events_lost).
type streamLine struct {
	ID    string          `json:"id"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
	Error *Error          `json:"error"`
}

type eventData struct {
	PaneID         string `json:"pane_id"`
	PreviousPaneID string `json:"previous_pane_id"`
	TabID          string `json:"tab_id"`
	WorkspaceID    string `json:"workspace_id"`
	Agent          string `json:"agent"`
	AgentStatus    string `json:"agent_status"`
	Pane           *struct {
		PaneID      string `json:"pane_id"`
		TabID       string `json:"tab_id"`
		WorkspaceID string `json:"workspace_id"`
	} `json:"pane"`
}

// kindOf maps both spellings of each event name (underscore lifecycle
// names and dotted subscription names) explicitly; nothing is normalised
// (T-03-18). Unknown names map to "".
func kindOf(name string) EventKind {
	switch name {
	case "pane_closed", "pane.closed":
		return KindPaneClosed
	case "pane_moved", "pane.moved":
		return KindPaneMoved
	case "pane_agent_detected", "pane.agent_detected":
		return KindAgentDetected
	case "pane_exited", "pane.exited":
		return KindPaneExited
	case "tab_closed", "tab.closed":
		return KindTabClosed
	case "workspace_closed", "workspace.closed":
		return KindWorkspaceClosed
	case "pane.agent_status_changed", "pane_agent_status_changed":
		return KindAgentStatusChanged
	}
	return ""
}

// Next reads one event line. An error line (events_lost included) is
// returned as *Error and closes the stream; EOF or a read failure is
// *UnreachableError. A malformed line returns a plain decode error and
// the stream stays usable. An unknown event returns Event{Kind: ""}.
func (s *Stream) Next() (Event, error) {
	line, err := readLine(s.br)
	if err != nil {
		if errors.Is(err, errLineTooLong) {
			_ = s.Close()
			return Event{}, &UnreachableError{Socket: s.socket, Err: err}
		}
		return Event{}, &UnreachableError{Socket: s.socket, Err: err}
	}
	var sl streamLine
	if err := json.Unmarshal(line, &sl); err != nil {
		return Event{}, fmt.Errorf("herdr: decode event: %w", err)
	}
	if sl.Error != nil {
		_ = s.Close()
		return Event{}, sl.Error
	}
	kind := kindOf(sl.Event)
	if kind == "" {
		return Event{}, nil
	}
	var d eventData
	if len(sl.Data) > 0 {
		if err := json.Unmarshal(sl.Data, &d); err != nil {
			return Event{}, fmt.Errorf("herdr: decode event %s: %w", sl.Event, err)
		}
	}
	ev := Event{Kind: kind, PaneID: d.PaneID, TabID: d.TabID, WorkspaceID: d.WorkspaceID,
		Agent: d.Agent, AgentStatus: d.AgentStatus}
	if kind == KindPaneMoved {
		ev.PreviousPaneID = d.PreviousPaneID
		if d.Pane != nil {
			ev.PaneID = d.Pane.PaneID
			ev.TabID = d.Pane.TabID
			ev.WorkspaceID = d.Pane.WorkspaceID
		}
	}
	return ev, nil
}

// Close closes the connection; a blocked Next returns. Safe to call more
// than once and concurrently with Next.
func (s *Stream) Close() error {
	var err error
	s.once.Do(func() { err = s.conn.Close() })
	return err
}
