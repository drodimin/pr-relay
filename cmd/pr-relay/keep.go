package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/model"
)

func newKeepCmd() *cobra.Command {
	var pane string
	cmd := &cobra.Command{
		Use:   "keep <PR-URL> [--pane ID]",
		Short: "Renew this pane's subscription to a pull request (check in)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKeep(cmd.Context(), cmd.OutOrStdout(), daemonCaller(cmd.ErrOrStderr()), os.Getenv, args[0], pane)
		},
	}
	cmd.Flags().StringVar(&pane, "pane", "", "act on herdr pane `ID` instead of HERDR_PANE_ID")
	return cmd
}

// runKeep renews the target pane's subscription.
func runKeep(ctx context.Context, stdout io.Writer, call caller, getenv func(string) string, link, pane string) error {
	ref, err := model.ParsePRLink(link)
	if err != nil {
		return err
	}
	target, err := resolveTarget(getenv, pane)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx, callTimeout)
	defer cancel()
	var res ctl.KeepResult
	if err := call(ctx, ctl.MethodKeep, ctl.KeepParams{URL: ref.URL(), Target: target}, &res); err != nil {
		return err
	}
	fmt.Fprint(stdout, formatKeep(res, time.Now()))
	return nil
}
