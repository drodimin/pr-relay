package ctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// writeTimeout bounds writing the response line.
const writeTimeout = 2 * time.Second

// Handler serves one method. Returning *Error chooses the code the client
// sees; any other error is reported as CodeInternal (or CodeTimeout when the
// method deadline expired).
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// ServerOptions configures NewServer.
type ServerOptions struct {
	// Self is the daemon identity, sent in every response and compared with
	// each request's client identity by the version gate.
	Self Peer
	// Handlers maps method names to handlers. hello and shutdown are built
	// in and cannot be overridden.
	Handlers map[string]Handler
	// OnShutdown runs in its own goroutine after the shutdown reply is
	// written.
	OnShutdown func()
	// Logger defaults to a discarding logger. Only the method and the error
	// code are ever logged (T-04-15).
	Logger *slog.Logger
	// MethodTimeout overrides the per-method handler deadline. nil, or a
	// non-positive return, means DefaultMethodTimeout.
	MethodTimeout func(method string) time.Duration
}

// DefaultMethodTimeout is the handler deadline per method: sub 8 s, poll
// 15 s, everything else 3 s.
func DefaultMethodTimeout(method string) time.Duration {
	switch method {
	case MethodSub:
		return 8 * time.Second
	case MethodPoll:
		return 15 * time.Second
	default:
		return 3 * time.Second
	}
}

// Server answers control requests: one request line and one response line
// per connection.
type Server struct {
	o   ServerOptions
	log *slog.Logger

	base   context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	ln     net.Listener
	closed bool
	wg     sync.WaitGroup
}

// NewServer returns a Server for o.
func NewServer(o ServerOptions) *Server {
	s := &Server{o: o, log: o.Logger}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.base, s.cancel = context.WithCancel(context.Background())
	return s
}

// Serve accepts connections on ln until Close, serving each in its own
// goroutine. It returns nil after Close and the accept error otherwise.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.ln = ln
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.isClosed() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if !s.begin() {
			_ = c.Close()
			continue
		}
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

// ServeConn serves exactly one request on c and closes it. After Close it
// closes c without reading.
func (s *Server) ServeConn(c net.Conn) {
	if !s.begin() {
		_ = c.Close()
		return
	}
	defer s.wg.Done()
	s.handle(c)
}

// Close stops accepting connections and waits up to drain for in-flight
// requests to finish. Handlers still running after drain have their context
// cancelled, Close waits up to drain once more for them to return, and then
// reports a timeout. The second wait means callers can release resources
// handlers use (the store, the socket lock) only after handlers have
// returned, unless a handler ignores cancellation for longer (review CR-02).
func (s *Server) Close(drain time.Duration) error {
	s.mu.Lock()
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(drain)
	defer t.Stop()
	select {
	case <-done:
		s.cancel()
		return err
	case <-t.C:
		s.cancel()
		t2 := time.NewTimer(drain)
		defer t2.Stop()
		select {
		case <-done:
		case <-t2.C:
		}
		return fmt.Errorf("ctl: drain timed out after %s", drain)
	}
}

func (s *Server) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(ReadTimeout))
	line, err := readLine(bufio.NewReaderSize(c, 64<<10))
	if err != nil {
		if errors.Is(err, errLineTooLong) {
			s.reply(c, "", "", nil, &Error{Code: CodeBadRequest, Message: err.Error()})
		}
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(c, "", "", nil, &Error{Code: CodeBadRequest, Message: "malformed request"})
		return
	}
	if req.Method != MethodHello && req.Method != MethodShutdown &&
		(req.Client.Version != s.o.Self.Version || req.Client.Protocol != s.o.Self.Protocol) {
		s.reply(c, req.ID, req.Method, nil, &Error{Code: CodeVersionMismatch, Message: fmt.Sprintf(
			"cli %s/%d, daemon %s/%d", req.Client.Version, req.Client.Protocol, s.o.Self.Version, s.o.Self.Protocol)})
		return
	}
	switch req.Method {
	case MethodHello:
		s.reply(c, req.ID, req.Method, HelloResult{}, nil)
		return
	case MethodShutdown:
		s.reply(c, req.ID, req.Method, ShutdownResult{}, nil)
		if s.o.OnShutdown != nil {
			go s.o.OnShutdown()
		}
		return
	}
	h, ok := s.o.Handlers[req.Method]
	if !ok || h == nil {
		s.reply(c, req.ID, req.Method, nil, &Error{Code: CodeBadRequest, Message: fmt.Sprintf("unknown method %q", req.Method)})
		return
	}
	res, err := s.call(req.Method, h, req.Params)
	if err != nil {
		s.reply(c, req.ID, req.Method, nil, toError(err))
		return
	}
	s.reply(c, req.ID, req.Method, res, nil)
}

func (s *Server) call(method string, h Handler, params json.RawMessage) (res any, err error) {
	timeout := time.Duration(0)
	if s.o.MethodTimeout != nil {
		timeout = s.o.MethodTimeout(method)
	}
	if timeout <= 0 {
		timeout = DefaultMethodTimeout(method)
	}
	ctx, cancel := context.WithTimeout(s.base, timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, &Error{Code: CodeInternal, Message: fmt.Sprintf("handler panic: %v", r)}
		}
	}()
	res, err = h(ctx, params)
	if err != nil && !isCtlError(err) && errors.Is(err, context.DeadlineExceeded) {
		return nil, &Error{Code: CodeTimeout, Message: method + " timed out"}
	}
	return res, err
}

func isCtlError(err error) bool {
	var ce *Error
	return errors.As(err, &ce)
}

func toError(err error) *Error {
	var ce *Error
	if errors.As(err, &ce) {
		return ce
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// reply writes one response line. Failures are only logged: the client has
// gone away and there is nothing else to do.
func (s *Server) reply(c net.Conn, id, method string, result any, e *Error) {
	resp := Response{ID: id, Error: e, Daemon: s.o.Self}
	if e == nil {
		b, err := json.Marshal(result)
		if err != nil {
			resp.Error = &Error{Code: CodeInternal, Message: "encode result"}
		} else {
			resp.Result = b
		}
	}
	code := ""
	if resp.Error != nil {
		code = resp.Error.Code
	}
	s.log.Debug("ctl request", "method", method, "code", code)
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.Write(append(b, '\n')); err != nil {
		s.log.Debug("ctl reply failed", "method", method, "err", err)
	}
}
