// shedd is the shed daemon: an SSH gateway on localhost that creates and
// brokers access to local microVMs, exe.dev-style. Build with `make build`
// — the binary must be codesigned with the virtualization entitlement.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:          "shedd",
		Short:        "shed daemon — local microVMs over ssh",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serve()
		},
	}
	root.AddCommand(cmdServe(), cmdInstall(), cmdDoctor())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "shedd:", err)
		os.Exit(1)
	}
}
