package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// dialTimeout bounds connecting to the herdr socket.
const dialTimeout = 2 * time.Second

// callTimeout is the read/write deadline of an ordinary request.
const callTimeout = 5 * time.Second

// waitSlack is added to a server-side wait timeout (agent.wait, or the wait
// clause of agent.prompt) to get the client read deadline, so herdr's own
// timeout reply arrives before the client gives up.
const waitSlack = 5 * time.Second

// maxLine caps one response line (T-03-05). pane.list on a busy session can
// be large, so this is well above bufio.Scanner's 64 KiB default.
const maxLine = 4 << 20

// DialFunc opens a connection to the herdr socket at path socket. Tests
// inject a net.Pipe-backed dialer (herdrtest.Server.Dialer) so they can run
// inside a testing/synctest bubble.
type DialFunc func(ctx context.Context, socket string) (net.Conn, error)

// Options configures NewClient. Socket is required. Dial defaults to
// DialUnix and Logger to a discarding logger.
type Options struct {
	Socket string
	Dial   DialFunc
	Logger *slog.Logger
}

// Client sends one-shot requests to one herdr socket. herdr answers one
// request per connection, so every call dials afresh. It is safe for
// concurrent use.
type Client struct {
	socket string
	dial   DialFunc
	log    *slog.Logger
	seq    atomic.Uint64
}

// NewClient returns a Client for o.Socket. The socket path is cleaned so
// that different spellings of one path share one key.
func NewClient(o Options) (*Client, error) {
	if o.Socket == "" {
		return nil, errors.New("herdr: empty socket path")
	}
	c := &Client{socket: filepath.Clean(o.Socket), dial: o.Dial, log: o.Logger}
	if c.dial == nil {
		c.dial = DialUnix
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// Socket returns the cleaned socket path.
func (c *Client) Socket() string { return c.socket }

// DialUnix connects to a herdr unix socket after checking that path is a
// unix socket owned by the current user (T-03-04). Anything else is refused,
// so a planted file or another user's socket is never dialed.
func DialUnix(ctx context.Context, socket string) (net.Conn, error) {
	fi, err := os.Lstat(socket)
	if err != nil {
		return nil, fmt.Errorf("herdr: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("herdr: %s: not a unix socket", socket)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("herdr: %s: not owned by current user", socket)
	}
	d := net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, "unix", socket)
}

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// Call sends one request and decodes its result into out (when non-nil).
// A herdr error reply is returned as *Error; any failure to reach herdr or
// get a full reply is *UnreachableError. A cancelled ctx closes the
// connection and returns the ctx error.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	return c.callWithTimeout(ctx, method, params, out, callTimeout)
}

func (c *Client) callWithTimeout(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("herdr: %s: %w", method, err)
	}
	start := time.Now()
	err := c.roundTrip(ctx, method, params, out, timeout)
	c.log.Debug("herdr call", "method", method, "socket", c.socket, "duration", time.Since(start),
		"code", Code(err), "unreachable", IsUnreachable(err))
	return err
}

func (c *Client) roundTrip(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if params == nil {
		params = struct{}{}
	}
	id := "pr-relay-" + strconv.FormatUint(c.seq.Add(1), 10)
	b, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("herdr: encode %s: %w", method, err)
	}

	conn, err := c.dial(ctx, c.socket)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("herdr: %s: %w", method, ctx.Err())
		}
		return &UnreachableError{Socket: c.socket, Err: err}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	dl := time.Now().Add(timeout)
	if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
		dl = cd
	}
	_ = conn.SetDeadline(dl)

	fail := func(err error) error {
		if ctx.Err() != nil {
			return fmt.Errorf("herdr: %s: %w", method, ctx.Err())
		}
		return &UnreachableError{Socket: c.socket, Err: err}
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fail(err)
	}

	br := bufio.NewReaderSize(conn, 64<<10)
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > maxLine {
			return fmt.Errorf("herdr: decode %s: response line exceeds %d bytes", method, maxLine)
		}
		line = append(line, chunk...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return fail(err)
	}

	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("herdr: decode %s: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if resp.ID != id {
		return fmt.Errorf("herdr: decode %s: response id %q, want %q", method, resp.ID, id)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("herdr: decode %s result: %w", method, err)
		}
	}
	return nil
}

// envelope is the typed result object shared by the helpers.
type envelope struct {
	Type  string     `json:"type"`
	Agent *AgentInfo `json:"agent"`
	Pane  *PaneInfo  `json:"pane"`
	Panes []PaneInfo `json:"panes"`
}

func checkType(method, got, want string) error {
	if got != want {
		return fmt.Errorf("herdr: decode %s: result type %q, want %q", method, got, want)
	}
	return nil
}

func (c *Client) agentCall(ctx context.Context, method string, params any, want string, timeout time.Duration) (AgentInfo, error) {
	var env envelope
	if err := c.callWithTimeout(ctx, method, params, &env, timeout); err != nil {
		return AgentInfo{}, err
	}
	if err := checkType(method, env.Type, want); err != nil {
		return AgentInfo{}, err
	}
	if env.Agent == nil {
		return AgentInfo{}, fmt.Errorf("herdr: decode %s: missing agent", method)
	}
	return *env.Agent, nil
}

// AgentGet returns the agent in pane. A missing pane or a bare shell is
// CodeAgentNotFound (never CodePaneNotFound; use PaneGet to tell them apart).
func (c *Client) AgentGet(ctx context.Context, pane string) (AgentInfo, error) {
	return c.agentCall(ctx, "agent.get", map[string]any{"target": pane}, "agent_info", callTimeout)
}

// AgentWait blocks until the agent in pane reaches one of the until
// statuses, herdr's timeout elapses (CodeTimeout), or the pinned occupant
// goes away (CodeAgentNotRunning). until must be non-empty (D-03: a wait
// without until returns at once while blocked) and timeout positive; the
// client read deadline is timeout + waitSlack.
func (c *Client) AgentWait(ctx context.Context, pane string, until []string, timeout time.Duration) (AgentInfo, error) {
	if len(until) == 0 {
		return AgentInfo{}, errors.New("herdr: agent.wait requires a non-empty until list")
	}
	if timeout <= 0 {
		return AgentInfo{}, errors.New("herdr: agent.wait requires a positive timeout")
	}
	ms := max(timeout.Milliseconds(), 1)
	params := map[string]any{"target": pane, "until": until, "timeout_ms": ms}
	return c.agentCall(ctx, "agent.wait", params, "agent_info", time.Duration(ms)*time.Millisecond+waitSlack)
}

// AgentPrompt sends text to the agent in p.Target. A blocked agent answers
// CodeAgentBlocked without receiving input. With p.Wait set, the read
// deadline is the wait timeout + waitSlack.
func (c *Client) AgentPrompt(ctx context.Context, p PromptParams) (AgentInfo, error) {
	timeout := callTimeout
	if p.Wait != nil {
		timeout = time.Duration(p.Wait.TimeoutMs)*time.Millisecond + waitSlack
	}
	return c.agentCall(ctx, "agent.prompt", p, "agent_prompted", timeout)
}

// PaneGet returns pane. A missing pane is CodePaneNotFound; a bare shell is
// returned with Status "unknown" and no agent.
func (c *Client) PaneGet(ctx context.Context, pane string) (PaneInfo, error) {
	var env envelope
	if err := c.Call(ctx, "pane.get", map[string]any{"pane_id": pane}, &env); err != nil {
		return PaneInfo{}, err
	}
	if err := checkType("pane.get", env.Type, "pane_info"); err != nil {
		return PaneInfo{}, err
	}
	if env.Pane == nil {
		return PaneInfo{}, errors.New("herdr: decode pane.get: missing pane")
	}
	return *env.Pane, nil
}

// PaneList returns every pane in the session.
func (c *Client) PaneList(ctx context.Context) ([]PaneInfo, error) {
	var env envelope
	if err := c.Call(ctx, "pane.list", struct{}{}, &env); err != nil {
		return nil, err
	}
	if err := checkType("pane.list", env.Type, "pane_list"); err != nil {
		return nil, err
	}
	return env.Panes, nil
}

// Ping returns herdr's version and protocol.
func (c *Client) Ping(ctx context.Context) (Pong, error) {
	var res struct {
		Type string `json:"type"`
		Pong
	}
	if err := c.Call(ctx, "ping", struct{}{}, &res); err != nil {
		return Pong{}, err
	}
	if err := checkType("ping", res.Type, "pong"); err != nil {
		return Pong{}, err
	}
	return res.Pong, nil
}
