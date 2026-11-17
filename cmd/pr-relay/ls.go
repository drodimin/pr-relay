package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/herdr"
)

func newLsCmd() *cobra.Command {
	var all bool
	var pane string
	cmd := &cobra.Command{
		Use:   "ls [--all] [--pane ID]",
		Short: "List this pane's subscriptions (every pane with --all or outside herdr)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLs(cmd.Context(), cmd.OutOrStdout(), daemonCaller(cmd.ErrOrStderr()), os.Getenv, all, pane)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list the subscriptions of every pane")
	cmd.Flags().StringVar(&pane, "pane", "", "list herdr pane `ID` instead of HERDR_PANE_ID")
	return cmd
}

// runLs lists subscriptions. Inside herdr (or with --pane) it shows only that
// pane; with --all, or outside herdr with no --pane, it shows every pane
// with a PANE column (D-21).
func runLs(ctx context.Context, stdout io.Writer, call caller, getenv func(string) string, all bool, pane string) error {
	all = all || (pane == "" && !herdr.InHerdr(getenv))
	var params ctl.LsParams
	if all {
		params.All = true
	} else {
		target, err := resolveTarget(getenv, pane)
		if err != nil {
			return err
		}
		params.Target = target
	}
	ctx, cancel := withTimeout(ctx, callTimeout)
	defer cancel()
	var res ctl.LsResult
	if err := call(ctx, ctl.MethodLs, params, &res); err != nil {
		return err
	}
	_, _ = fmt.Fprint(stdout, formatLs(res, all))
	return nil
}
