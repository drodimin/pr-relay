package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/model"
)

func newPollCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "poll <PR-URL>",
		Short: "Poll a watched pull request now and deliver anything new to its subscribers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPoll(cmd.Context(), cmd.OutOrStdout(), daemonCaller(cmd.ErrOrStderr()), args[0])
		},
	}
}

// runPoll forces an immediate poll (D-09). It needs no pane: results go to
// every subscriber. The deadline exceeds the server's 15 s poll budget.
func runPoll(ctx context.Context, stdout io.Writer, call caller, link string) error {
	ref, err := model.ParsePRLink(link)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx, pollTimeout)
	defer cancel()
	var res ctl.PollResult
	if err := call(ctx, ctl.MethodPoll, ctl.PollParams{URL: ref.URL()}, &res); err != nil {
		return err
	}
	_, _ = fmt.Fprint(stdout, formatPoll(res))
	return nil
}
