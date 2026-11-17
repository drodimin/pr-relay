package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/ctl"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon health: version, auth, rate limit, herdr delivery and recent cancellations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(cmd.Context(), cmd.OutOrStdout(), daemonCaller(cmd.ErrOrStderr()))
		},
	}
}

// runStatus prints the daemon's health (D-23, DMN-05). Like every command it
// starts the daemon when none is running (DMN-01).
func runStatus(ctx context.Context, stdout io.Writer, call caller) error {
	ctx, cancel := withTimeout(ctx, callTimeout)
	defer cancel()
	var res ctl.StatusResult
	if err := call(ctx, ctl.MethodStatus, nil, &res); err != nil {
		return err
	}
	_, _ = fmt.Fprint(stdout, formatStatus(res))
	return nil
}
