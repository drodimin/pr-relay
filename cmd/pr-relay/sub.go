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

func newSubCmd() *cobra.Command {
	var events []string
	var pane string
	cmd := &cobra.Command{
		Use:   "sub <PR-URL> [--events TYPE,...] [--pane ID]",
		Short: "Subscribe this pane to a pull request; returns immediately and updates arrive in this pane",
		Long: "Subscribe this herdr pane to a GitHub pull request. The command returns at once;\n" +
			"the pr-relay daemon (started automatically if needed) watches the PR and pushes\n" +
			"short updates into this pane. Do not poll or wait: carry on with other work.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSub(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				daemonCaller(cmd.ErrOrStderr()), os.Getenv, args[0], events, pane)
		},
	}
	cmd.Flags().StringSliceVar(&events, "events", nil, "only deliver these event `TYPES` (comma-separated; ci and all are aliases)")
	cmd.Flags().StringVar(&pane, "pane", "", "deliver to herdr pane `ID` instead of HERDR_PANE_ID")
	return cmd
}

// runSub validates the link, events and target locally, before any daemon
// dial (T-04-46), then subscribes within the hard sub deadline (SUB-01).
func runSub(ctx context.Context, stdout, stderr io.Writer, call caller, getenv func(string) string,
	link string, events []string, pane string) error {
	ref, err := model.ParsePRLink(link)
	if err != nil {
		return err
	}
	ev, err := parseEvents(events)
	if err != nil {
		return err
	}
	target, err := resolveTarget(getenv, pane)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx, subTimeout)
	defer cancel()
	var res ctl.SubResult
	if err := call(ctx, ctl.MethodSub, ctl.SubParams{URL: ref.URL(), Events: ev, Target: target}, &res); err != nil {
		return err
	}
	_, _ = fmt.Fprint(stdout, formatSub(res, time.Now()))
	if res.Warning != "" {
		_, _ = fmt.Fprintln(stderr, "warning: "+res.Warning)
	}
	return nil
}
