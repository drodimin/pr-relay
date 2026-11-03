package herdr_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
)

func pipeClient(t *testing.T, s *herdrtest.Server) *herdr.Client {
	t.Helper()
	c, err := herdr.NewClient(herdr.Options{Socket: "/fake/h.sock", Dial: s.Dialer()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func u64(v uint64) *uint64 { return &v }

func TestNewClientRequiresSocket(t *testing.T) {
	if _, err := herdr.NewClient(herdr.Options{}); err == nil {
		t.Fatal("NewClient with empty socket succeeded")
	}
	c, err := herdr.NewClient(herdr.Options{Socket: "/tmp/x//h.sock/"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket() != "/tmp/x/h.sock" {
		t.Fatalf("Socket() = %q, want cleaned path", c.Socket())
	}
}

func TestAgentGet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Agent: "claude",
			Session: "s1", Status: herdr.StatusIdle, StateSeq: 7, CompletionSeq: u64(3), TerminalID: "term1"})
		s.SetPane(herdrtest.PaneState{ID: "w1:p2", Agent: "codex", Status: herdr.StatusWorking})
		c := pipeClient(t, s)
		ctx := context.Background()

		info, err := c.AgentGet(ctx, "w1:p1")
		if err != nil {
			t.Fatal(err)
		}
		if info.PaneID != "w1:p1" || info.TabID != "w1:t1" || info.WorkspaceID != "w1" || info.Agent != "claude" ||
			info.Status != herdr.StatusIdle || info.StateChangeSeq != 7 || info.TerminalID != "term1" {
			t.Fatalf("info = %+v", info)
		}
		if info.AgentSession == nil || info.AgentSession.Value != "s1" {
			t.Fatalf("session = %+v", info.AgentSession)
		}
		if info.CompletionSeq == nil || *info.CompletionSeq != 3 {
			t.Fatalf("completion_seq = %v", info.CompletionSeq)
		}

		info, err = c.AgentGet(ctx, "w1:p2")
		if err != nil {
			t.Fatal(err)
		}
		if info.CompletionSeq != nil || info.AgentSession != nil {
			t.Fatalf("unscripted completion/session not nil: %+v", info)
		}
	})
}

func TestMissingPaneErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		c := pipeClient(t, s)
		ctx := context.Background()
		_, err := c.AgentGet(ctx, "w9:p9")
		if herdr.Code(err) != herdr.CodeAgentNotFound || herdr.IsUnreachable(err) {
			t.Fatalf("AgentGet missing: %v (code %q)", err, herdr.Code(err))
		}
		var he *herdr.Error
		if !errors.As(err, &he) || he.Message == "" {
			t.Fatalf("not a *herdr.Error with message: %v", err)
		}
		_, err = c.PaneGet(ctx, "w9:p9")
		if herdr.Code(err) != herdr.CodePaneNotFound {
			t.Fatalf("PaneGet missing: %v", err)
		}
	})
}

func TestPaneGetAndList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p2", WorkspaceID: "w1"}) // bare shell
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Session: "s1", Status: herdr.StatusDone})
		c := pipeClient(t, s)
		ctx := context.Background()
		p, err := c.PaneGet(ctx, "w1:p2")
		if err != nil {
			t.Fatal(err)
		}
		if p.PaneID != "w1:p2" || p.Agent != "" || p.AgentSession != nil || p.Status != herdr.StatusUnknown {
			t.Fatalf("bare pane = %+v", p)
		}
		panes, err := c.PaneList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(panes) != 2 || panes[0].PaneID != "w1:p1" || panes[0].Agent != "claude" ||
			panes[0].AgentSession == nil || panes[0].AgentSession.Value != "s1" || panes[0].Status != herdr.StatusDone {
			t.Fatalf("panes = %+v", panes)
		}
		pong, err := c.Ping(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if pong.Protocol != herdr.Protocol || pong.Version == "" {
			t.Fatalf("pong = %+v", pong)
		}
	})
}

func TestUnreachableAfterStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Status: herdr.StatusIdle})
		c := pipeClient(t, s)
		ctx := context.Background()
		s.Stop()
		calls := map[string]func() error{
			"agent.get":    func() error { _, err := c.AgentGet(ctx, "w1:p1"); return err },
			"agent.wait":   func() error { _, err := c.AgentWait(ctx, "w1:p1", []string{"idle"}, time.Second); return err },
			"agent.prompt": func() error { _, err := c.AgentPrompt(ctx, herdr.PromptParams{Target: "w1:p1", Text: "x"}); return err },
			"pane.get":     func() error { _, err := c.PaneGet(ctx, "w1:p1"); return err },
			"pane.list":    func() error { _, err := c.PaneList(ctx); return err },
			"ping":         func() error { _, err := c.Ping(ctx); return err },
		}
		for name, f := range calls {
			err := f()
			if !herdr.IsUnreachable(err) || herdr.Code(err) != "" {
				t.Errorf("%s after Stop: %v (unreachable %v, code %q)", name, err, herdr.IsUnreachable(err), herdr.Code(err))
			}
		}
	})
}

func TestUnreachableEOFMidWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Status: herdr.StatusWorking})
		c := pipeClient(t, s)
		errc := make(chan error, 1)
		go func() {
			_, err := c.AgentWait(context.Background(), "w1:p1", []string{"idle"}, time.Minute)
			errc <- err
		}()
		synctest.Wait()
		s.Stop() // herdr goes away before replying
		if err := <-errc; !herdr.IsUnreachable(err) {
			t.Fatalf("err = %v, want unreachable", err)
		}
	})
}

func TestAgentWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Status: herdr.StatusWorking})
		c := pipeClient(t, s)
		ctx := context.Background()

		start := time.Now()
		_, err := c.AgentWait(ctx, "w1:p1", []string{"idle", "done"}, 800*time.Millisecond)
		if herdr.Code(err) != herdr.CodeTimeout {
			t.Fatalf("err = %v, want timeout", err)
		}
		if d := time.Since(start); d != 800*time.Millisecond {
			t.Fatalf("elapsed %v, want 800ms", d)
		}
		reqs := s.Requests()
		var p map[string]any
		if err := json.Unmarshal(reqs[len(reqs)-1].Params, &p); err != nil {
			t.Fatal(err)
		}
		if p["timeout_ms"] != float64(800) || p["target"] != "w1:p1" {
			t.Fatalf("wait params = %v", p)
		}
		if u, ok := p["until"].([]any); !ok || len(u) != 2 || u[0] != "idle" || u[1] != "done" {
			t.Fatalf("until = %v", p["until"])
		}

		// Satisfied by a later status change.
		go func() {
			time.Sleep(2 * time.Second)
			s.SetStatus("w1:p1", herdr.StatusIdle)
		}()
		info, err := c.AgentWait(ctx, "w1:p1", []string{"idle", "done"}, time.Minute)
		if err != nil || info.Status != herdr.StatusIdle {
			t.Fatalf("wait = %+v, %v", info, err)
		}

		// D-03: never wait without until.
		n := s.CallCount("agent.wait")
		if _, err := c.AgentWait(ctx, "w1:p1", nil, time.Minute); err == nil {
			t.Fatal("AgentWait with empty until succeeded")
		}
		if _, err := c.AgentWait(ctx, "w1:p1", []string{"idle"}, 0); err == nil {
			t.Fatal("AgentWait with zero timeout succeeded")
		}
		if s.CallCount("agent.wait") != n {
			t.Fatal("rejected AgentWait reached herdr")
		}
	})
}

func TestAgentWaitCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Status: herdr.StatusWorking})
		c := pipeClient(t, s)
		ctx, cancel := context.WithCancel(context.Background())
		errc := make(chan error, 1)
		go func() {
			_, err := c.AgentWait(ctx, "w1:p1", []string{"idle"}, 5*time.Minute)
			errc <- err
		}()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		start := time.Now()
		cancel()
		err := <-errc
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if herdr.IsUnreachable(err) {
			t.Fatalf("cancelled wait reported unreachable: %v", err)
		}
		if time.Since(start) != 0 {
			t.Fatalf("cancel took %v", time.Since(start))
		}
	})
}

func TestAgentPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Session: "s1", Status: herdr.StatusIdle})
		s.SetPane(herdrtest.PaneState{ID: "w1:p2", Agent: "claude", Status: herdr.StatusBlocked})
		c := pipeClient(t, s)
		ctx := context.Background()
		text := "[pr-relay] o/r#1 ci.failed: build https://github.com/o/r/pull/1"
		info, err := c.AgentPrompt(ctx, herdr.PromptParams{Target: "w1:p1", Text: text,
			Wait: &herdr.WaitOpts{Until: []string{herdr.StatusWorking}, TimeoutMs: 5000}})
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != herdr.StatusWorking {
			t.Fatalf("prompted agent status = %q", info.Status)
		}
		p := s.Prompts()
		if len(p) != 1 || p[0].Target != "w1:p1" || p[0].Text != text || p[0].Wait == nil ||
			len(p[0].Wait.Until) != 1 || p[0].Wait.Until[0] != "working" || p[0].Wait.TimeoutMs != 5000 {
			t.Fatalf("prompts = %+v", p)
		}

		_, err = c.AgentPrompt(ctx, herdr.PromptParams{Target: "w1:p2", Text: "x"})
		if herdr.Code(err) != herdr.CodeAgentBlocked {
			t.Fatalf("blocked prompt err = %v", err)
		}
		if len(s.Prompts()) != 1 {
			t.Fatal("blocked prompt was recorded")
		}
		// No wait clause is omitted on the wire.
		reqs := s.Requests()
		if strings.Contains(string(reqs[len(reqs)-1].Params), "wait") {
			t.Fatalf("nil Wait sent: %s", reqs[len(reqs)-1].Params)
		}
	})
}

func TestNoConnectionReuse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := herdrtest.New(t)
		s.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Status: herdr.StatusIdle})
		c := pipeClient(t, s)
		for range 10 {
			if _, err := c.AgentGet(context.Background(), "w1:p1"); err != nil {
				t.Fatal(err)
			}
		}
		if n := s.Conns(); n != 10 {
			t.Fatalf("connections = %d, want 10", n)
		}
	})
}

func TestDialUnixGuard(t *testing.T) {
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tc := range []struct{ path, want string }{
		{file, "not a unix socket"},
		{dir, "not a unix socket"},
		{filepath.Join(dir, "missing.sock"), ""},
	} {
		conn, err := herdr.DialUnix(ctx, tc.path)
		if err == nil {
			conn.Close()
			t.Errorf("DialUnix(%s) succeeded", tc.path)
			continue
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("DialUnix(%s) = %v, want %q", tc.path, err, tc.want)
		}
		c, _ := herdr.NewClient(herdr.Options{Socket: tc.path})
		if _, err := c.Ping(ctx); !herdr.IsUnreachable(err) {
			t.Errorf("Ping via %s: %v, want unreachable", tc.path, err)
		}
	}

	s := herdrtest.New(t)
	path := s.ListenUnix()
	c, err := herdr.NewClient(herdr.Options{Socket: path})
	if err != nil {
		t.Fatal(err)
	}
	pong, err := c.Ping(ctx)
	if err != nil || pong.Protocol != herdr.Protocol {
		t.Fatalf("Ping over unix: %+v, %v", pong, err)
	}
	s.Stop()
	if _, err := c.Ping(ctx); !herdr.IsUnreachable(err) {
		t.Fatalf("Ping after Stop: %v, want unreachable", err)
	}
	s.Restart()
	if _, err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping after Restart: %v", err)
	}
}

func TestOversizedLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dial := func(ctx context.Context, socket string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				buf := make([]byte, 4096)
				_, _ = server.Read(buf) // the request
				chunk := []byte(strings.Repeat("x", 64<<10))
				for {
					if _, err := server.Write(chunk); err != nil {
						return
					}
				}
			}()
			return client, nil
		}
		c, err := herdr.NewClient(herdr.Options{Socket: "/fake", Dial: dial})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Ping(context.Background())
		if err == nil || herdr.IsUnreachable(err) || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("err = %v, want decode error", err)
		}
	})
}

func TestMalformedResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dial := func(ctx context.Context, socket string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				buf := make([]byte, 4096)
				_, _ = server.Read(buf)
				_, _ = server.Write([]byte("{not json\n"))
			}()
			return client, nil
		}
		c, _ := herdr.NewClient(herdr.Options{Socket: "/fake", Dial: dial})
		_, err := c.Ping(context.Background())
		if err == nil || herdr.IsUnreachable(err) || herdr.Code(err) != "" {
			t.Fatalf("err = %v, want decode error", err)
		}
	})
}
