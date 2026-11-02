// Command pr-relay watches GitHub pull requests and pushes filtered updates
// into herdr panes.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func versionString() string {
	return "pr-relay " + version
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pr-relay",
		Short:         "Watch GitHub pull requests and push updates into herdr panes",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the pr-relay version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionString())
		},
	})
	root.AddCommand(newDevCmd())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "pr-relay: %v\n", err)
		os.Exit(1)
	}
}
