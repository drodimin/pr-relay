package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/drodimin/pr-relay/internal/autostart"
	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/daemon"
)

func TestRunEnsure(t *testing.T) {
	okEnsure := func(context.Context) error { return nil }
	okHello := func(context.Context) (ctl.Peer, error) { return ctl.Peer{PID: 42, Version: "v0.1.0"}, nil }

	t.Run("prints pid and version", func(t *testing.T) {
		var out bytes.Buffer
		if err := runEnsure(context.Background(), &out, okEnsure, okHello); err != nil {
			t.Fatalf("runEnsure: %v", err)
		}
		if got, want := out.String(), "daemon running (pid 42, v0.1.0)\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("ensure error returned unchanged", func(t *testing.T) {
		boom := errors.New("boom")
		var out bytes.Buffer
		helloCalled := false
		err := runEnsure(context.Background(), &out, func(context.Context) error { return boom },
			func(ctx context.Context) (ctl.Peer, error) { helloCalled = true; return okHello(ctx) })
		if err != boom {
			t.Errorf("err = %v, want %v", err, boom)
		}
		if out.Len() != 0 {
			t.Errorf("printed %q on error", out.String())
		}
		if helloCalled {
			t.Error("hello called after ensure failed")
		}
	})

	t.Run("hello error returned", func(t *testing.T) {
		boom := errors.New("no hello")
		var out bytes.Buffer
		err := runEnsure(context.Background(), &out, okEnsure,
			func(context.Context) (ctl.Peer, error) { return ctl.Peer{}, boom })
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want %v", err, boom)
		}
		if out.Len() != 0 {
			t.Errorf("printed %q on error", out.String())
		}
	})

	t.Run("bounded by a deadline", func(t *testing.T) {
		var out bytes.Buffer
		err := runEnsure(context.Background(), &out, func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("ensure ctx has no deadline")
			}
			return nil
		}, okHello)
		if err != nil {
			t.Fatalf("runEnsure: %v", err)
		}
	})
}

// TestAlreadyRunningMsgMatches guards the string autostart matches on: a
// spawned daemon that loses the lock reports daemon.ErrAlreadyRunning, and
// autostart cannot import daemon.
func TestAlreadyRunningMsgMatches(t *testing.T) {
	if got, want := daemon.ErrAlreadyRunning.Error(), autostart.AlreadyRunningMsg; got != want {
		t.Errorf("daemon.ErrAlreadyRunning = %q, autostart.AlreadyRunningMsg = %q", got, want)
	}
}
