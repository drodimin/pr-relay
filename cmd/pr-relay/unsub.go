package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/model"
)

func newUnsubCmd() *cobra.Command {
	var all bool
	var pane string
	cmd := &cobra.Command{
		Use:   "unsub <PR-URL> | --all [--pane ID]",
		Short: "Unsubscribe this pane from a pull request, or from all with --all",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			link := ""
			if len(args) == 1 {
				link = args[0]
			}
			return runUnsub(cmd.Context(), cmd.OutOrStdout(), daemonCaller(cmd.ErrOrStderr()), os.Getenv, link, all, pane)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "remove every subscription of this pane")
	cmd.Flags().StringVar(&pane, "pane", "", "act on herdr pane `ID` instead of HERDR_PANE_ID")
	return cmd
}

// runUnsub removes one subscription of the target pane, or all of them
// (D-24). Nothing matching is not an error.
func runUnsub(ctx context.Context, stdout io.Writer, call caller, getenv func(string) string,
	link string, all bool, pane string) error {
	if (link == "") == !all {
		return errors.New("pass exactly one of a pull request URL or --all")
	}
	params := ctl.UnsubParams{All: all}
	var ref model.PRRef
	if link != "" {
		var err error
		if ref, err = model.ParsePRLink(link); err != nil {
			return err
		}
		params.URL = ref.URL()
	}
	target, err := resolveTarget(getenv, pane)
	if err != nil {
		return err
	}
	params.Target = target
	ctx, cancel := withTimeout(ctx, callTimeout)
	defer cancel()
	var res ctl.UnsubResult
	if err := call(ctx, ctl.MethodUnsub, params, &res); err != nil {
		return err
	}
	switch {
	case res.Removed == 0:
		_, _ = fmt.Fprintln(stdout, "not subscribed")
	case !all:
		_, _ = fmt.Fprintf(stdout, "unsubscribed %s\n", ref)
	case res.Removed == 1:
		_, _ = fmt.Fprintln(stdout, "unsubscribed 1 subscription")
	default:
		_, _ = fmt.Fprintf(stdout, "unsubscribed %d subscriptions\n", res.Removed)
	}
	return nil
}
