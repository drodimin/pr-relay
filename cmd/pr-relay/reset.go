package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/ctl"
)

func newResetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset [--yes]",
		Short: "Drop every subscription on this machine (asks for confirmation, or pass --yes)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReset(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				daemonCaller(cmd.ErrOrStderr()), cmd.InOrStdin(), stdinIsTTY(), yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

// stdinIsTTY reports whether stdin is a terminal (stdlib check, no x/term).
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runReset drops every subscription on the machine (D-25). Without --yes it
// needs an interactive "y"/"yes" on a TTY, so an agent cannot wipe other
// agents' subscriptions by accident (T-04-44).
func runReset(ctx context.Context, stdout, stderr io.Writer, call caller, stdin io.Reader, isTTY, yes bool) error {
	if !yes {
		if !isTTY {
			return errors.New("reset drops every subscription on this machine; re-run with --yes")
		}
		_, _ = fmt.Fprint(stderr, "drop every pr-relay subscription on this machine? [y/N] ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return errors.New("aborted")
		}
	}
	ctx, cancel := withTimeout(ctx, callTimeout)
	defer cancel()
	var res ctl.ResetResult
	if err := call(ctx, ctl.MethodReset, nil, &res); err != nil {
		return err
	}
	noun := "subscriptions"
	if res.Removed == 1 {
		noun = "subscription"
	}
	_, _ = fmt.Fprintf(stdout, "removed %d %s\n", res.Removed, noun)
	return nil
}
