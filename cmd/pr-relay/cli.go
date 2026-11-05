package main

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/drodimin/pr-relay/internal/autostart"
	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/herdr"
)

// Hard deadlines for daemon round trips. sub must never hang an agent's
// tool call (SUB-01, T-04-47). poll waits for a real GitHub round trip, and
// the server gives it 15 s, so the CLI waits a little longer.
const (
	subTimeout  = 10 * time.Second
	callTimeout = 10 * time.Second
	pollTimeout = 20 * time.Second
)

// errNotInPane is the D-10/SUB-03 error for pane-scoped commands run outside
// herdr without --pane.
var errNotInPane = errors.New("not inside a herdr pane (HERDR_PANE_ID unset); pass --pane")

// caller sends one request to the daemon. Commands take a caller so tests
// can run them without a daemon.
type caller func(ctx context.Context, method string, params, result any) error

// daemonCaller is the real caller: autostart.Call spawns the daemon when
// none is running and handles a version mismatch with one restart and one
// stderr note (D-17).
func daemonCaller(stderr io.Writer) caller {
	return func(ctx context.Context, method string, params, result any) error {
		_, err := autostart.Call(ctx, autostart.Options{Version: version, Stderr: stderr}, method, params, result)
		return err
	}
}

// resolveTarget returns the delivery target of a pane-scoped command: the
// pane from --pane or HERDR_PANE_ID and the herdr socket of the calling
// environment (SUB-03). It reads the environment only through getenv.
func resolveTarget(getenv func(string) string, paneFlag string) (ctl.Target, error) {
	pane := paneFlag
	if pane == "" {
		pane = herdr.PaneFromEnv(getenv)
	}
	if pane == "" {
		return ctl.Target{}, errNotInPane
	}
	socket, err := herdr.ResolveSocket(getenv)
	if err != nil {
		return ctl.Target{}, err
	}
	return ctl.Target{HerdrSocket: socket, PaneID: pane}, nil
}

// cliError turns a daemon error reply into its message alone, so main prints
// "pr-relay: MESSAGE" instead of the wire code. Other errors pass through.
func cliError(err error) error {
	var ce *ctl.Error
	if errors.As(err, &ce) {
		if ce.Message == "" {
			return errors.New(ce.Code)
		}
		return errors.New(ce.Message)
	}
	return err
}

// withTimeout bounds ctx by d unless it already has an earlier deadline.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, d)
}
