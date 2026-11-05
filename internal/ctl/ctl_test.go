package ctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var daemonSelf = Peer{Version: "0.4.0", Protocol: Protocol, PID: 42}

// pipeDialer serves every dial with srv.ServeConn over net.Pipe.
func pipeDialer(srv *Server) DialFunc {
	return func(ctx context.Context, socket string) (net.Conn, error) {
		client, server := net.Pipe()
		go srv.ServeConn(server)
		return client, nil
	}
}

func newPair(o ServerOptions, version string) (*Server, *Client) {
	if o.Self == (Peer{}) {
		o.Self = daemonSelf
	}
	srv := NewServer(o)
	return srv, NewClient("pipe", version, pipeDialer(srv))
}

func TestRoundTrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, c := newPair(ServerOptions{Handlers: map[string]Handler{
			MethodStatus: func(ctx context.Context, _ json.RawMessage) (any, error) {
				return StatusResult{Version: "0.4.0", PID: 42, PRs: 3}, nil
			},
			MethodSub: func(ctx context.Context, p json.RawMessage) (any, error) {
				var sp SubParams
				if err := json.Unmarshal(p, &sp); err != nil {
					return nil, &Error{Code: CodeBadRequest, Message: err.Error()}
				}
				return SubResult{PR: "o/r#1", URL: sp.URL, Created: true, Events: sp.Events}, nil
			},
		}}, "0.4.0")
		var st StatusResult
		peer, err := c.Call(t.Context(), MethodStatus, nil, &st)
		if err != nil {
			t.Fatalf("Call status: %v", err)
		}
		if peer != daemonSelf {
			t.Fatalf("daemon peer = %+v, want %+v", peer, daemonSelf)
		}
		if st.PRs != 3 || st.PID != 42 {
			t.Fatalf("status = %+v", st)
		}
		var sr SubResult
		if _, err := c.Call(t.Context(), MethodSub, SubParams{URL: "https://github.com/o/r/pull/1",
			Events: []string{"ci"}, Target: Target{HerdrSocket: "/s", PaneID: "w1:p1"}}, &sr); err != nil {
			t.Fatalf("Call sub: %v", err)
		}
		if !sr.Created || sr.URL != "https://github.com/o/r/pull/1" || len(sr.Events) != 1 {
			t.Fatalf("sub = %+v", sr)
		}
	})
}

func TestVersionGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var subs atomic.Int32
		shut := make(chan struct{})
		_, c := newPair(ServerOptions{
			Handlers: map[string]Handler{MethodSub: func(context.Context, json.RawMessage) (any, error) {
				subs.Add(1)
				return SubResult{}, nil
			}},
			OnShutdown: func() { close(shut) },
		}, "0.3.0")

		peer, err := c.Call(t.Context(), MethodSub, SubParams{URL: "u"}, nil)
		var vm *VersionMismatchError
		if !errors.As(err, &vm) {
			t.Fatalf("err = %v, want *VersionMismatchError", err)
		}
		if vm.Daemon.Version != "0.4.0" || vm.Client.Version != "0.3.0" || peer.Version != "0.4.0" {
			t.Fatalf("mismatch = %+v, peer %+v", vm, peer)
		}
		if Code(err) != CodeVersionMismatch {
			t.Fatalf("Code = %q", Code(err))
		}
		if n := subs.Load(); n != 0 {
			t.Fatalf("sub handler ran %d times despite mismatch", n)
		}

		// hello and shutdown are exempt.
		if p, err := c.Call(t.Context(), MethodHello, nil, &HelloResult{}); err != nil || p.Version != "0.4.0" {
			t.Fatalf("hello: peer %+v err %v", p, err)
		}
		if _, err := c.Call(t.Context(), MethodShutdown, nil, &ShutdownResult{}); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
		<-shut
	})
}

func TestVersionGateProtocol(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var subs atomic.Int32
		_, c := newPair(ServerOptions{
			Self: Peer{Version: "0.4.0", Protocol: Protocol + 1},
			Handlers: map[string]Handler{MethodSub: func(context.Context, json.RawMessage) (any, error) {
				subs.Add(1)
				return nil, nil
			}},
		}, "0.4.0")
		_, err := c.Call(t.Context(), MethodSub, nil, nil)
		var vm *VersionMismatchError
		if !errors.As(err, &vm) || vm.Daemon.Protocol != Protocol+1 {
			t.Fatalf("err = %v, want protocol mismatch", err)
		}
		if subs.Load() != 0 {
			t.Fatal("sub handler ran despite protocol mismatch")
		}
	})
}

func TestUnknownMethod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, c := newPair(ServerOptions{}, "0.4.0")
		_, err := c.Call(t.Context(), "bogus", nil, nil)
		if Code(err) != CodeBadRequest {
			t.Fatalf("err = %v, want bad_request", err)
		}
	})
}

func TestHandlerErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, c := newPair(ServerOptions{Handlers: map[string]Handler{
			MethodKeep: func(context.Context, json.RawMessage) (any, error) {
				return nil, &Error{Code: CodeNotFound, Message: "no such subscription"}
			},
			MethodLs: func(context.Context, json.RawMessage) (any, error) {
				return nil, errors.New("boom")
			},
			MethodPoll: func(ctx context.Context, _ json.RawMessage) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			MethodReset: func(context.Context, json.RawMessage) (any, error) {
				panic("kaboom")
			},
		}}, "0.4.0")
		if _, err := c.Call(t.Context(), MethodKeep, nil, nil); Code(err) != CodeNotFound {
			t.Fatalf("keep err = %v, want not_found", err)
		}
		_, err := c.Call(t.Context(), MethodLs, nil, nil)
		if Code(err) != CodeInternal || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("ls err = %v, want internal boom", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		start := time.Now()
		if _, err := c.Call(ctx, MethodPoll, nil, nil); Code(err) != CodeTimeout {
			t.Fatalf("poll err = %v, want timeout", err)
		}
		if d := time.Since(start); d != 15*time.Second {
			t.Fatalf("poll timed out after %s, want 15s", d)
		}
		if _, err := c.Call(t.Context(), MethodReset, nil, nil); Code(err) != CodeInternal {
			t.Fatalf("reset err = %v, want internal", err)
		}
	})
}

// rawExchange writes payload on a fresh pipe conn to srv (concurrently, so a
// large write cannot deadlock the reply) and returns the response line.
func rawExchange(t *testing.T, srv *Server, payload []byte) (Response, error) {
	t.Helper()
	client, server := net.Pipe()
	go srv.ServeConn(server)
	defer client.Close()
	go func() { _, _ = client.Write(payload) }()
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	return resp, nil
}

func TestMalformedJSON(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := NewServer(ServerOptions{Self: daemonSelf})
		resp, err := rawExchange(t, srv, []byte("{not json\n"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != CodeBadRequest || resp.Daemon != daemonSelf {
			t.Fatalf("resp = %+v", resp)
		}
	})
}

func TestLineCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := NewServer(ServerOptions{Self: daemonSelf})
		big := make([]byte, 2<<20)
		for i := range big {
			big[i] = 'a'
		}
		big[len(big)-1] = '\n'
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			srv.ServeConn(server)
			close(done)
		}()
		go func() { _, _ = client.Write(big) }()
		br := bufio.NewReader(client)
		line, err := br.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != CodeBadRequest {
			t.Fatalf("resp = %+v, want bad_request", resp)
		}
		<-done
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("conn still open after oversized line: %v", err)
		}
		client.Close()
	})
}

func TestReadTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := NewServer(ServerOptions{Self: daemonSelf})
		client, server := net.Pipe()
		defer client.Close()
		go srv.ServeConn(server)
		start := time.Now()
		_, err := client.Read(make([]byte, 1))
		if err == nil {
			t.Fatal("read succeeded, want closed conn")
		}
		if d := time.Since(start); d != ReadTimeout {
			t.Fatalf("closed after %s, want %s", d, ReadTimeout)
		}
	})
}

func TestCloseDrains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		var finished atomic.Bool
		srv, c := newPair(ServerOptions{Handlers: map[string]Handler{
			MethodStatus: func(ctx context.Context, _ json.RawMessage) (any, error) {
				close(started)
				time.Sleep(time.Second)
				finished.Store(true)
				return StatusResult{}, nil
			},
		}}, "0.4.0")
		errc := make(chan error, 1)
		go func() {
			_, err := c.Call(context.Background(), MethodStatus, nil, nil)
			errc <- err
		}()
		<-started
		if err := srv.Close(2 * time.Second); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !finished.Load() {
			t.Fatal("Close returned before the in-flight handler finished")
		}
		if err := <-errc; err != nil {
			t.Fatalf("in-flight call: %v", err)
		}
		// New connections are refused after Close.
		if _, err := c.Call(context.Background(), MethodStatus, nil, nil); !IsUnreachable(err) {
			t.Fatalf("call after Close: %v, want unreachable", err)
		}
	})
}

func TestCloseDrainTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		srv, c := newPair(ServerOptions{Handlers: map[string]Handler{
			MethodStatus: func(ctx context.Context, _ json.RawMessage) (any, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}, MethodTimeout: func(string) time.Duration { return time.Hour }}, "0.4.0")
		errc := make(chan error, 1)
		go func() {
			_, err := c.Call(context.Background(), MethodStatus, nil, nil)
			errc <- err
		}()
		<-started
		start := time.Now()
		if err := srv.Close(time.Second); err == nil {
			t.Fatal("Close returned nil with a stuck handler")
		}
		if d := time.Since(start); d != time.Second {
			t.Fatalf("Close waited %s, want 1s", d)
		}
		// Close cancels the handler context, so the call ends.
		if err := <-errc; err == nil {
			t.Fatal("stuck call returned no error")
		}
	})
}

// TestShutdownDrain covers review CR-02: after the drain timeout Close
// cancels handlers and then waits a second bounded period for them, so the
// daemon never closes the store while a handler can still save.
func TestShutdownDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const drain = time.Second
		started := make(chan struct{})
		var finished atomic.Bool
		srv, c := newPair(ServerOptions{Handlers: map[string]Handler{
			MethodStatus: func(ctx context.Context, _ json.RawMessage) (any, error) {
				close(started)
				// Ignores cancellation for 1.5x the drain period.
				time.Sleep(drain * 3 / 2)
				finished.Store(true)
				return StatusResult{}, nil
			},
		}, MethodTimeout: func(string) time.Duration { return time.Hour }}, "0.4.0")
		go func() { _, _ = c.Call(context.Background(), MethodStatus, nil, nil) }()
		<-started
		err := srv.Close(drain)
		if !finished.Load() {
			t.Fatal("Close returned while a handler was still running")
		}
		if err == nil || !strings.Contains(err.Error(), "drain timed out") {
			t.Fatalf("Close err = %v, want drain timed out", err)
		}
	})
}

// shortDir returns a short temp dir so unix socket paths fit in sun_path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestUnixSocketServe(t *testing.T) {
	sock := filepath.Join(shortDir(t), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerOptions{Self: daemonSelf, Handlers: map[string]Handler{
		MethodLs: func(context.Context, json.RawMessage) (any, error) {
			return LsResult{Rows: []LsRow{{PR: "o/r#1", PaneID: "w1:p1"}}}, nil
		},
	}})
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	c := NewClient(sock, "0.4.0", nil)
	var res LsResult
	if _, err := c.Call(t.Context(), MethodLs, LsParams{All: true}, &res); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].PR != "o/r#1" {
		t.Fatalf("ls = %+v", res)
	}
	if err := srv.Close(time.Second); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve returned %v, want nil after Close", err)
	}
}

func TestDialGuard(t *testing.T) {
	dir := shortDir(t)

	t.Run("missing", func(t *testing.T) {
		c := NewClient(filepath.Join(dir, "none.sock"), "0.4.0", nil)
		_, err := c.Call(t.Context(), MethodHello, nil, nil)
		if !IsUnreachable(err) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want unreachable ErrNotExist", err)
		}
	})

	t.Run("regular file", func(t *testing.T) {
		p := filepath.Join(dir, "file.sock")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewClient(p, "0.4.0", nil).Call(t.Context(), MethodHello, nil, nil)
		if !IsUnreachable(err) || !strings.Contains(err.Error(), "not a unix socket") {
			t.Fatalf("err = %v, want not a unix socket", err)
		}
	})

	t.Run("stale socket", func(t *testing.T) {
		p := filepath.Join(dir, "stale.sock")
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		ln.Close()
		_, err = NewClient(p, "0.4.0", nil).Call(t.Context(), MethodHello, nil, nil)
		if !IsUnreachable(err) {
			t.Fatalf("err = %v, want unreachable", err)
		}
	})
}
