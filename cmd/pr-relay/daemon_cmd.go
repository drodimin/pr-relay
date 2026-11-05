package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/autostart"
	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/daemon"
)

// newDaemonCmd runs the daemon in the foreground (the auto-start entry
// point), ensures a background daemon with --ensure (the herdr plugin
// [[startup]] hook, D-08), and holds the stop and restart controls (D-19).
func newDaemonCmd() *cobra.Command {
	var ensure bool
	cmd := &cobra.Command{
		Use:   "daemon [--ensure]",
		Short: "Run the pr-relay daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ensure {
				// autostart.Restart is a no-op when this version already
				// answers, spawns a detached daemon when none does, and
				// replaces a mismatched version under the spawn lock (D-01).
				o := autostartOptions(cmd.ErrOrStderr())
				return runEnsure(cmd.Context(), cmd.OutOrStdout(),
					func(ctx context.Context) error { return autostart.Restart(ctx, o) },
					func(ctx context.Context) (ctl.Peer, error) {
						return autostart.Call(ctx, o, ctl.MethodHello, nil, nil)
					})
			}
			return runDaemonForeground(cmd.Context(), cmd.OutOrStdout(), func(ctx context.Context) error {
				return daemon.Run(ctx, daemon.Options{Version: version, GraphQLEndpoint: os.Getenv(envGraphQLURL)})
			})
		},
	}
	cmd.Flags().BoolVar(&ensure, "ensure", false,
		"start the daemon in the background unless one with this version is running, then exit")
	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemonStop(cmd.Context(), cmd.OutOrStdout(), func(ctx context.Context) (bool, error) {
				return autostart.Stop(ctx, autostartOptions(cmd.ErrOrStderr()))
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon (starts it when none is running)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o := autostartOptions(cmd.ErrOrStderr())
			return runDaemonRestart(cmd.Context(), cmd.OutOrStdout(),
				func(ctx context.Context) error {
					// autostart.Restart is a no-op when the running daemon
					// already has this version (it exists for the version
					// gate), so stop first to force a fresh process.
					if _, err := autostart.Stop(ctx, o); err != nil {
						return err
					}
					return autostart.Restart(ctx, o)
				},
				func(ctx context.Context) (ctl.Peer, error) {
					return autostart.Call(ctx, o, ctl.MethodHello, nil, nil)
				})
		},
	})
	return cmd
}

func autostartOptions(stderr io.Writer) autostart.Options {
	return autostart.Options{Version: version, Stderr: stderr}
}

// runDaemonForeground runs the daemon. Another daemon already serving the
// socket is not an error: it prints a note and exits 0, so racing starters
// (plugin startup, auto-start) stay quiet.
func runDaemonForeground(ctx context.Context, stdout io.Writer, run func(context.Context) error) error {
	err := run(ctx)
	if errors.Is(err, daemon.ErrAlreadyRunning) {
		fmt.Fprintln(stdout, "daemon already running")
		return nil
	}
	return err
}

// runDaemonStop stops the daemon; nothing running is not an error.
func runDaemonStop(ctx context.Context, stdout io.Writer, stop func(context.Context) (bool, error)) error {
	ctx, cancel := withTimeout(ctx, callTimeout)
	defer cancel()
	was, err := stop(ctx)
	if err != nil {
		return err
	}
	if was {
		fmt.Fprintln(stdout, "daemon stopped")
	} else {
		fmt.Fprintln(stdout, "daemon not running")
	}
	return nil
}

// runDaemonRestart restarts the daemon and reports the new daemon's pid.
func runDaemonRestart(ctx context.Context, stdout io.Writer, restart func(context.Context) error,
	hello func(context.Context) (ctl.Peer, error)) error {
	ctx, cancel := withTimeout(ctx, 2*callTimeout)
	defer cancel()
	if err := restart(ctx); err != nil {
		return err
	}
	peer, err := hello(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "daemon restarted (pid %d)\n", peer.PID)
	return nil
}

// runEnsure makes sure a daemon of this version is running, then reports
// it in one line and returns. Bounded like runDaemonRestart (T-05-13).
func runEnsure(ctx context.Context, stdout io.Writer, ensure func(context.Context) error,
	hello func(context.Context) (ctl.Peer, error)) error {
	ctx, cancel := withTimeout(ctx, 2*callTimeout)
	defer cancel()
	if err := ensure(ctx); err != nil {
		return err
	}
	peer, err := hello(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "daemon running (pid %d, %s)\n", peer.PID, peer.Version)
	return nil
}
