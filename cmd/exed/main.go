// exed is the devexe daemon: an SSH gateway on localhost that creates and
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
		Use:          "exed",
		Short:        "devexe daemon — local microVMs over ssh",
		SilenceUsage: true,
	}
	root.AddCommand(cmdServe(), cmdInstall(), cmdDoctor())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "exed:", err)
		os.Exit(1)
	}
}
