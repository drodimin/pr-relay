package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/ctl"
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
