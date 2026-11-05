package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/daemon"
)

// envMap is an injected getenv over a fixed map.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var inPane = map[string]string{
	"HERDR_PANE_ID":     "w1:p1",
	"HERDR_SOCKET_PATH": "/s/h.sock",
	"HOME":              "/home/u",
}

var outsideHerdr = map[string]string{"HOME": "/home/u"}

// fakeCaller records every daemon call and answers through reply.
type fakeCaller struct {
	methods []string
	params  []any
	reply   func(method string, params, result any) error
}

func (f *fakeCaller) call(_ context.Context, method string, params, result any) error {
	f.methods = append(f.methods, method)
	f.params = append(f.params, params)
	if f.reply == nil {
		return nil
	}
	return f.reply(method, params, result)
}

func TestResolveTarget(t *testing.T) {
	got, err := resolveTarget(envMap(inPane), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := (ctl.Target{HerdrSocket: "/s/h.sock", PaneID: "w1:p1"}); got != want {
		t.Errorf("env target = %+v, want %+v", got, want)
	}

	got, err = resolveTarget(envMap(inPane), "w2:p3")
	if err != nil {
		t.Fatal(err)
	}
	if want := (ctl.Target{HerdrSocket: "/s/h.sock", PaneID: "w2:p3"}); got != want {
		t.Errorf("--pane target = %+v, want %+v", got, want)
	}

	_, err = resolveTarget(envMap(outsideHerdr), "")
	if err == nil || err.Error() != "not inside a herdr pane (HERDR_PANE_ID unset); pass --pane" {
		t.Errorf("outside herdr err = %v", err)
	}

	got, err = resolveTarget(envMap(outsideHerdr), "w3:p1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (ctl.Target{HerdrSocket: "/home/u/.config/herdr/herdr.sock", PaneID: "w3:p1"}); got != want {
		t.Errorf("outside herdr --pane target = %+v, want %+v", got, want)
	}
}

func TestSubLocalValidation(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		link   string
		events []string
		pane   string
		want   string
	}{
		{"issue", inPane, "https://github.com/o/r/issues/1", nil, "", "is an issue, not a pull request"},
		{"gitlab", inPane, "https://gitlab.com/o/r/pull/1", nil, "", "only github.com is supported"},
		{"bogus events", inPane, "https://github.com/o/r/pull/1", []string{"bogus"}, "", "unknown event type"},
		{"no pane", outsideHerdr, "https://github.com/o/r/pull/1", nil, "", "not inside a herdr pane"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{}
			var out, errb bytes.Buffer
			err := runSub(context.Background(), &out, &errb, fc.call, envMap(tc.env), tc.link, tc.events, tc.pane)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if len(fc.methods) != 0 {
				t.Errorf("daemon called %v before local validation passed", fc.methods)
			}
		})
	}
}

func TestSubCallsDaemon(t *testing.T) {
	fc := &fakeCaller{reply: func(_ string, _, result any) error {
		r := result.(*ctl.SubResult)
		*r = ctl.SubResult{PR: "o/r#1", URL: "https://github.com/o/r/pull/1", Created: true, Warning: "herdr is not reachable"}
		return nil
	}}
	var out, errb bytes.Buffer
	if err := runSub(context.Background(), &out, &errb, fc.call, envMap(inPane), "https://github.com/o/r/pull/1/files", []string{"ci"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(fc.methods) != 1 || fc.methods[0] != ctl.MethodSub {
		t.Fatalf("methods = %v", fc.methods)
	}
	p := fc.params[0].(ctl.SubParams)
	if p.URL != "https://github.com/o/r/pull/1" || p.PaneID != "w1:p1" || p.HerdrSocket != "/s/h.sock" ||
		strings.Join(p.Events, ",") != "ci.failed,ci.settled" {
		t.Errorf("params = %+v", p)
	}
	if !strings.HasPrefix(out.String(), "subscribed o/r#1") {
		t.Errorf("stdout = %q", out.String())
	}
	if errb.String() != "warning: herdr is not reachable\n" {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestCliError(t *testing.T) {
	err := cliError(&ctl.Error{Code: "not_found", Message: "o/r#12 not found or not visible"})
	if err.Error() != "o/r#12 not found or not visible" {
		t.Errorf("ctl.Error rendered %q", err)
	}
	wrapped := cliError(errors.Join(errors.New("ctx"), &ctl.Error{Code: "bad_request", Message: "bad"}))
	if wrapped.Error() != "bad" {
		t.Errorf("wrapped ctl.Error rendered %q", wrapped)
	}
	ue := &ctl.UnreachableError{Socket: "/x.sock", Err: errors.New("connection refused")}
	if got := cliError(ue); got.Error() != ue.Error() {
		t.Errorf("unreachable rendered %q, want %q", got, ue.Error())
	}
	if cliError(nil) != nil {
		t.Error("cliError(nil) != nil")
	}
}

func TestUnsubNotSubscribed(t *testing.T) {
	removed := 0
	fc := &fakeCaller{reply: func(_ string, _, result any) error {
		result.(*ctl.UnsubResult).Removed = removed
		return nil
	}}
	var out bytes.Buffer
	if err := runUnsub(context.Background(), &out, fc.call, envMap(inPane), "https://github.com/o/r/pull/12", false, ""); err != nil {
		t.Fatal(err)
	}
	if out.String() != "not subscribed\n" {
		t.Errorf("stdout = %q", out.String())
	}

	removed, out = 1, bytes.Buffer{}
	if err := runUnsub(context.Background(), &out, fc.call, envMap(inPane), "https://github.com/o/r/pull/12", false, ""); err != nil {
		t.Fatal(err)
	}
	if out.String() != "unsubscribed o/r#12\n" {
		t.Errorf("stdout = %q", out.String())
	}

	removed, out = 2, bytes.Buffer{}
	if err := runUnsub(context.Background(), &out, fc.call, envMap(inPane), "", true, ""); err != nil {
		t.Fatal(err)
	}
	if out.String() != "unsubscribed 2 subscriptions\n" {
		t.Errorf("stdout = %q", out.String())
	}
	last := fc.params[len(fc.params)-1].(ctl.UnsubParams)
	if !last.All || last.URL != "" || last.PaneID != "w1:p1" {
		t.Errorf("--all params = %+v", last)
	}
}

func TestUnsubArgs(t *testing.T) {
	fc := &fakeCaller{}
	var out bytes.Buffer
	if err := runUnsub(context.Background(), &out, fc.call, envMap(inPane), "", false, ""); err == nil {
		t.Error("neither link nor --all accepted")
	}
	if err := runUnsub(context.Background(), &out, fc.call, envMap(inPane), "https://github.com/o/r/pull/1", true, ""); err == nil {
		t.Error("both link and --all accepted")
	}
	if err := runUnsub(context.Background(), &out, fc.call, envMap(outsideHerdr), "", true, ""); err == nil ||
		!strings.Contains(err.Error(), "not inside a herdr pane") {
		t.Errorf("outside herdr err = %v", err)
	}
	if len(fc.methods) != 0 {
		t.Errorf("daemon called %v", fc.methods)
	}
}

func TestKeepAndPollLocalValidation(t *testing.T) {
	fc := &fakeCaller{}
	var out bytes.Buffer
	if err := runKeep(context.Background(), &out, fc.call, envMap(outsideHerdr), "https://github.com/o/r/pull/1", ""); err == nil ||
		!strings.Contains(err.Error(), "not inside a herdr pane") {
		t.Errorf("keep outside herdr err = %v", err)
	}
	if err := runPoll(context.Background(), &out, fc.call, "https://github.com/o/r/issues/1"); err == nil {
		t.Error("poll accepted an issue link")
	}
	if len(fc.methods) != 0 {
		t.Errorf("daemon called %v", fc.methods)
	}
	// poll needs no pane.
	if err := runPoll(context.Background(), &out, fc.call, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatal(err)
	}
	if fc.methods[0] != ctl.MethodPoll || fc.params[0].(ctl.PollParams).URL != "https://github.com/o/r/pull/1" {
		t.Errorf("poll call = %v %+v", fc.methods, fc.params)
	}
}

func TestLsScope(t *testing.T) {
	fc := &fakeCaller{reply: func(_ string, _, result any) error {
		result.(*ctl.LsResult).Rows = []ctl.LsRow{{PR: "o/r#1", PaneID: "w1:p1"}}
		return nil
	}}
	var out bytes.Buffer
	if err := runLs(context.Background(), &out, fc.call, envMap(inPane), false, ""); err != nil {
		t.Fatal(err)
	}
	p := fc.params[0].(ctl.LsParams)
	if p.All || p.PaneID != "w1:p1" || p.HerdrSocket != "/s/h.sock" {
		t.Errorf("in-pane ls params = %+v", p)
	}
	if strings.Contains(out.String(), "PANE") {
		t.Errorf("in-pane ls printed the PANE column: %q", out.String())
	}

	out.Reset()
	if err := runLs(context.Background(), &out, fc.call, envMap(inPane), true, ""); err != nil {
		t.Fatal(err)
	}
	if p := fc.params[1].(ctl.LsParams); !p.All {
		t.Errorf("ls --all params = %+v", p)
	}

	out.Reset()
	if err := runLs(context.Background(), &out, fc.call, envMap(outsideHerdr), false, ""); err != nil {
		t.Fatal(err)
	}
	if p := fc.params[2].(ctl.LsParams); !p.All {
		t.Errorf("outside-herdr ls params = %+v", p)
	}
	if !strings.HasPrefix(out.String(), "PANE") {
		t.Errorf("outside-herdr ls lacks the PANE column: %q", out.String())
	}
}

func TestResetConfirm(t *testing.T) {
	reply := func(_ string, _, result any) error {
		result.(*ctl.ResetResult).Removed = 3
		return nil
	}

	fc := &fakeCaller{reply: reply}
	var out, errb bytes.Buffer
	err := runReset(context.Background(), &out, &errb, fc.call, strings.NewReader(""), false, false)
	if err == nil || err.Error() != "reset drops every subscription on this machine; re-run with --yes" {
		t.Errorf("non-TTY err = %v", err)
	}
	if len(fc.methods) != 0 {
		t.Errorf("non-TTY reset called %v", fc.methods)
	}

	if err := runReset(context.Background(), &out, &errb, fc.call, strings.NewReader(""), false, true); err != nil {
		t.Fatal(err)
	}
	if len(fc.methods) != 1 || fc.methods[0] != ctl.MethodReset || out.String() != "removed 3 subscriptions\n" {
		t.Errorf("--yes: methods %v, stdout %q", fc.methods, out.String())
	}

	for _, answer := range []string{"y\n", "YES\n"} {
		fc, out, errb = &fakeCaller{reply: reply}, bytes.Buffer{}, bytes.Buffer{}
		if err := runReset(context.Background(), &out, &errb, fc.call, strings.NewReader(answer), true, false); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if len(fc.methods) != 1 {
			t.Errorf("answer %q: methods %v", answer, fc.methods)
		}
		if !strings.Contains(errb.String(), "[y/N]") {
			t.Errorf("prompt = %q", errb.String())
		}
	}

	for _, answer := range []string{"n\n", "\n", "", "sure\n"} {
		fc, out, errb = &fakeCaller{reply: reply}, bytes.Buffer{}, bytes.Buffer{}
		err := runReset(context.Background(), &out, &errb, fc.call, strings.NewReader(answer), true, false)
		if err == nil || err.Error() != "aborted" {
			t.Errorf("answer %q: err = %v", answer, err)
		}
		if len(fc.methods) != 0 {
			t.Errorf("answer %q: methods %v", answer, fc.methods)
		}
	}
}

func TestRootCommands(t *testing.T) {
	root := newRootCmd()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
		if c.Name() == "daemon" {
			sub := map[string]bool{}
			for _, s := range c.Commands() {
				sub[s.Name()] = true
			}
			if !sub["stop"] || !sub["restart"] {
				t.Errorf("daemon subcommands = %v", sub)
			}
		}
		if c.Flags().Lookup("json") != nil {
			t.Errorf("%s offers --json (D-22)", c.Name())
		}
	}
	for _, name := range []string{"sub", "unsub", "keep", "poll", "ls", "status", "reset", "daemon", "version", "dev"} {
		if !have[name] {
			t.Errorf("missing command %q", name)
		}
	}
}

func TestDaemonCommands(t *testing.T) {
	var out bytes.Buffer
	if err := runDaemonStop(context.Background(), &out, func(context.Context) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if out.String() != "daemon not running\n" {
		t.Errorf("stop idle = %q", out.String())
	}
	out.Reset()
	if err := runDaemonStop(context.Background(), &out, func(context.Context) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if out.String() != "daemon stopped\n" {
		t.Errorf("stop running = %q", out.String())
	}

	out.Reset()
	run := func(context.Context) error { return fmt.Errorf("wrapped: %w", daemon.ErrAlreadyRunning) }
	if err := runDaemonForeground(context.Background(), &out, run); err != nil {
		t.Fatalf("already running err = %v", err)
	}
	if out.String() != "daemon already running\n" {
		t.Errorf("already running = %q", out.String())
	}
	boom := errors.New("boom")
	if err := runDaemonForeground(context.Background(), &out, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("run error = %v", err)
	}

	out.Reset()
	err := runDaemonRestart(context.Background(), &out,
		func(context.Context) error { return nil },
		func(context.Context) (ctl.Peer, error) { return ctl.Peer{PID: 4242}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "daemon restarted (pid 4242)\n" {
		t.Errorf("restart = %q", out.String())
	}
}
