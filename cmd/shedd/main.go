// shedd is the shed daemon: an SSH gateway on localhost that creates and
// brokers access to local microVMs, exe.dev-style. Build with `make build`
// — the binary must be codesigned with the virtualization entitlement.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fredrik/shed/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:          "shedd",
		Short:        "shed daemon — local microVMs over ssh",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		Version:      version.Current().String(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return serve()
		},
	}
	root.AddCommand(cmdServe(), cmdInstall(), cmdDoctor(), &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "shedd %s\n", version.Current())
		},
	})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "shedd:", err)
		os.Exit(1)
	}
}
