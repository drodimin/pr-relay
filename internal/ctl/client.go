package ctl

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// dialTimeout bounds connecting to the control socket.
const dialTimeout = 2 * time.Second

// defaultCallTimeout is the whole-call deadline when ctx has none. Callers
// of poll (15 s server-side) should pass a ctx with a longer deadline.
const defaultCallTimeout = 10 * time.Second

// DialFunc opens a connection to the control socket at path socket. Tests
// inject a net.Pipe-backed dialer so they can run inside testing/synctest.
type DialFunc func(ctx context.Context, socket string) (net.Conn, error)

// DialUnix connects to the control socket after checking that it is a unix
// socket owned by the current user (T-04-12). A planted file or another
// user's socket is never dialed.
func DialUnix(ctx context.Context, socket string) (net.Conn, error) {
	fi, err := os.Lstat(socket)
	if err != nil {
		return nil, fmt.Errorf("ctl: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("ctl: %s: not a unix socket", socket)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("ctl: %s: not owned by current user", socket)
	}
	d := net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, "unix", socket)
}

// Client sends one-shot requests to the daemon. Every call dials afresh. It
// is safe for concurrent use.
type Client struct {
	socket string
	self   Peer
	dial   DialFunc
}

// NewClient returns a Client for socket that identifies itself as version
// at the current Protocol. A nil dial means DialUnix.
func NewClient(socket, version string, dial DialFunc) *Client {
	if dial == nil {
		dial = DialUnix
	}
	return &Client{socket: socket, self: Peer{Version: version, Protocol: Protocol}, dial: dial}
}

// Socket returns the socket path the client dials.
func (c *Client) Socket() string { return c.socket }

// Call sends one request and decodes its result into result (when non-nil).
// It returns the daemon's Peer whenever a response arrived, including error
// responses. A daemon error is *Error, a version refusal is
// *VersionMismatchError, and any failure to reach the daemon or read a full
// reply is *UnreachableError. A cancelled ctx returns the ctx error.
func (c *Client) Call(ctx context.Context, method string, params, result any) (Peer, error) {
	if err := ctx.Err(); err != nil {
		return Peer{}, fmt.Errorf("ctl: %s: %w", method, err)
	}
	req := Request{ID: newID(), Method: method, Client: c.self}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Peer{}, fmt.Errorf("ctl: encode %s: %w", method, err)
		}
		req.Params = b
	}
	line, err := json.Marshal(req)
	if err != nil {
		return Peer{}, fmt.Errorf("ctl: encode %s: %w", method, err)
	}

	conn, err := c.dial(ctx, c.socket)
	if err != nil {
		if ctx.Err() != nil {
			return Peer{}, fmt.Errorf("ctl: %s: %w", method, ctx.Err())
		}
		return Peer{}, &UnreachableError{Socket: c.socket, Err: err}
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	dl := time.Now().Add(defaultCallTimeout)
	if cd, ok := ctx.Deadline(); ok {
		dl = cd
	}
	_ = conn.SetDeadline(dl)

	fail := func(err error) (Peer, error) {
		if ctx.Err() != nil {
			return Peer{}, fmt.Errorf("ctl: %s: %w", method, ctx.Err())
		}
		return Peer{}, &UnreachableError{Socket: c.socket, Err: err}
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fail(err)
	}
	raw, err := readLine(bufio.NewReaderSize(conn, 64<<10))
	if err != nil {
		if errors.Is(err, errLineTooLong) {
			return Peer{}, fmt.Errorf("ctl: decode %s: %w", method, err)
		}
		return fail(err)
	}

	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Peer{}, fmt.Errorf("ctl: decode %s: %w", method, err)
	}
	if resp.Error != nil {
		if resp.Error.Code == CodeVersionMismatch {
			return resp.Daemon, &VersionMismatchError{Client: c.self, Daemon: resp.Daemon}
		}
		return resp.Daemon, resp.Error
	}
	if resp.ID != req.ID {
		return resp.Daemon, fmt.Errorf("ctl: decode %s: response id %q, want %q", method, resp.ID, req.ID)
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return resp.Daemon, fmt.Errorf("ctl: decode %s result: %w", method, err)
		}
	}
	return resp.Daemon, nil
}

var errLineTooLong = fmt.Errorf("line exceeds %d bytes", MaxLine)

// readLine reads one '\n'-terminated line of at most MaxLine bytes.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > MaxLine {
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

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
