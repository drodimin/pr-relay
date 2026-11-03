package herdrtest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type rawResp struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// roundTrip writes lines on c, then reads every response line until EOF.
func roundTrip(t *testing.T, c net.Conn, lines ...string) []rawResp {
	t.Helper()
	defer c.Close()
	go func() {
		for _, l := range lines {
			if _, err := io.WriteString(c, l+"\n"); err != nil {
				return
			}
		}
	}()
	var out []rawResp
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return out
		}
		var r rawResp
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, r)
	}
}

func dial(t *testing.T, s *Server) net.Conn {
	t.Helper()
	c, err := s.Dialer()(context.Background(), "ignored")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func TestOneResponsePerConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(t)
		got := roundTrip(t, dial(t, s),
			`{"id":"a","method":"ping","params":{}}`,
			`{"id":"b","method":"ping","params":{}}`)
		if len(got) != 1 || got[0].ID != "a" || got[0].Error != nil {
			t.Fatalf("responses = %+v, want exactly one pong for id a", got)
		}
		if !strings.Contains(string(got[0].Result), `"pong"`) {
			t.Fatalf("result = %s", got[0].Result)
		}
		if n := s.CallCount("ping"); n != 1 {
			t.Fatalf("ping count = %d, want 1 (second request must be ignored)", n)
		}
	})
}

func TestOneResponsePerConnectionUnix(t *testing.T) {
	s := New(t)
	path := s.ListenUnix()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	got := roundTrip(t, c,
		`{"id":"a","method":"ping","params":{}}`,
		`{"id":"b","method":"ping","params":{}}`)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("responses = %+v, want one", got)
	}
}

func TestNonStringID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(t)
		got := roundTrip(t, dial(t, s), `{"id":5,"method":"ping","params":{}}`)
		if len(got) != 1 || got[0].ID != "" || got[0].Error == nil || got[0].Error.Code != "invalid_request" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestMissingPaneCodes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(t)
		s.SetPane(PaneState{ID: "w1:p2", Status: "idle"}) // bare shell
		cases := []struct{ req, code string }{
			{`{"id":"1","method":"agent.get","params":{"target":"w9:p9"}}`, "agent_not_found"},
			{`{"id":"1","method":"agent.prompt","params":{"target":"w9:p9","text":"x"}}`, "agent_not_found"},
			{`{"id":"1","method":"agent.wait","params":{"target":"w9:p9","until":["idle"],"timeout_ms":10}}`, "agent_not_found"},
			{`{"id":"1","method":"agent.get","params":{"target":"w1:p2"}}`, "agent_not_found"},
			{`{"id":"1","method":"pane.get","params":{"pane_id":"w9:p9"}}`, "pane_not_found"},
			{`{"id":"1","method":"nope","params":{}}`, "invalid_request"},
		}
		for _, c := range cases {
			got := roundTrip(t, dial(t, s), c.req)
			if len(got) != 1 || got[0].Error == nil || got[0].Error.Code != c.code {
				t.Errorf("%s: got %+v, want %s", c.req, got, c.code)
			}
		}
		// pane.get on a bare shell: pane_info, status unknown, no agent.
		got := roundTrip(t, dial(t, s), `{"id":"1","method":"pane.get","params":{"pane_id":"w1:p2"}}`)
		if len(got) != 1 || got[0].Error != nil {
			t.Fatalf("pane.get bare: %+v", got)
		}
		var res struct {
			Type string         `json:"type"`
			Pane map[string]any `json:"pane"`
		}
		_ = json.Unmarshal(got[0].Result, &res)
		if res.Type != "pane_info" || res.Pane["agent_status"] != "unknown" {
			t.Fatalf("pane.get bare = %s", got[0].Result)
		}
		if _, ok := res.Pane["agent"]; ok {
			t.Fatalf("bare shell pane carries agent: %s", got[0].Result)
		}
	})
}

func TestPromptBlockedRecordsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(t)
		s.SetPane(PaneState{ID: "w1:p1", Agent: "claude", Session: "s1", Status: "blocked"})
		got := roundTrip(t, dial(t, s), `{"id":"1","method":"agent.prompt","params":{"target":"w1:p1","text":"hi"}}`)
		if got[0].Error == nil || got[0].Error.Code != "agent_blocked" {
			t.Fatalf("got %+v", got)
		}
		if n := len(s.Prompts()); n != 0 {
			t.Fatalf("prompts = %d, want 0", n)
		}
		s.SetStatus("w1:p1", "idle")
		got = roundTrip(t, dial(t, s),
			`{"id":"2","method":"agent.prompt","params":{"target":"w1:p1","text":"hi","wait":{"until":["working"],"timeout_ms":5000}}}`)
		if got[0].Error != nil || !strings.Contains(string(got[0].Result), `"agent_prompted"`) {
			t.Fatalf("got %+v", got)
		}
		ps, _ := s.Pane("w1:p1")
		if ps.Status != "working" || ps.StateSeq != 2 {
			t.Fatalf("pane after prompt = %+v, want working seq 2", ps)
		}
		p := s.Prompts()
		if len(p) != 1 || p[0].Text != "hi" || p[0].Wait == nil || p[0].Wait.TimeoutMs != 5000 {
			t.Fatalf("prompts = %+v", p)
		}
	})
}

func TestWaitTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(t)
		s.SetPane(PaneState{ID: "w1:p1", Agent: "claude", Status: "idle"})
		start := time.Now()
		got := roundTrip(t, dial(t, s), `{"id":"1","method":"agent.wait","params":{"target":"w1:p1","until":["working"],"timeout_ms":800}}`)
		if len(got) != 1 || got[0].Error == nil || got[0].Error.Code != "timeout" {
			t.Fatalf("got %+v", got)
		}
		if d := time.Since(start); d != 800*time.Millisecond {
			t.Fatalf("elapsed %v, want exactly 800ms", d)
		}
	})
}

func TestWaitSemantics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(t)
		s.SetPane(PaneState{ID: "w1:p1", Agent: "claude", Session: "s1", Status: "blocked"})

		// S2a: no until while blocked returns at once.
		start := time.Now()
		got := roundTrip(t, dial(t, s), `{"id":"1","method":"agent.wait","params":{"target":"w1:p1","timeout_ms":60000}}`)
		if got[0].Error != nil || time.Since(start) != 0 {
			t.Fatalf("no-until wait: %+v after %v", got, time.Since(start))
		}

		// Already satisfied returns at once.
		got = roundTrip(t, dial(t, s), `{"id":"2","method":"agent.wait","params":{"target":"w1:p1","until":["blocked"],"timeout_ms":60000}}`)
		if got[0].Error != nil {
			t.Fatalf("satisfied wait: %+v", got)
		}

		// Blocks until the status changes.
		done := make(chan []rawResp)
		go func() {
			done <- roundTrip(t, dial(t, s), `{"id":"3","method":"agent.wait","params":{"target":"w1:p1","until":["idle","done"],"timeout_ms":60000}}`)
		}()
		time.Sleep(time.Second)
		synctest.Wait()
		s.SetStatus("w1:p1", "done")
		got = <-done
		if got[0].Error != nil || !strings.Contains(string(got[0].Result), `"done"`) {
			t.Fatalf("status wait: %+v", got)
		}

		// Occupant change: agent_not_running.
		go func() {
			done <- roundTrip(t, dial(t, s), `{"id":"4","method":"agent.wait","params":{"target":"w1:p1","until":["working"],"timeout_ms":60000}}`)
		}()
		synctest.Wait()
		s.SetOccupant("w1:p1", "codex", "s2")
		got = <-done
		if got[0].Error == nil || got[0].Error.Code != "agent_not_running" {
			t.Fatalf("occupant wait: %+v", got)
		}
	})
}

func TestStopRestart(t *testing.T) {
	s := New(t)
	path := s.ListenUnix()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("socket missing: %v", err)
	}
	s.Stop()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after Stop: %v, want not exist", err)
	}
	if _, err := s.Dialer()(context.Background(), path); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("Dialer after Stop: %v, want ECONNREFUSED", err)
	}
	if _, err := net.Dial("unix", path); err == nil {
		t.Fatal("unix dial after Stop succeeded")
	}
	s.Restart()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial after Restart: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	got := roundTrip(t, c, `{"id":"a","method":"ping","params":{}}`)
	if len(got) != 1 || got[0].Error != nil {
		t.Fatalf("ping after Restart: %+v", got)
	}
	pc, err := s.Dialer()(context.Background(), path)
	if err != nil {
		t.Fatalf("Dialer after Restart: %v", err)
	}
	pc.Close()
}

func TestFailNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(t)
		s.FailNext("ping", "agent_prompt_stalled")
		got := roundTrip(t, dial(t, s), `{"id":"1","method":"ping","params":{}}`)
		if got[0].Error == nil || got[0].Error.Code != "agent_prompt_stalled" {
			t.Fatalf("got %+v", got)
		}
		got = roundTrip(t, dial(t, s), `{"id":"1","method":"ping","params":{}}`)
		if got[0].Error != nil {
			t.Fatalf("second ping: %+v", got)
		}
		if c := s.Calls(); len(c) != 2 || c[0] != "ping" {
			t.Fatalf("calls = %v", c)
		}
	})
}
