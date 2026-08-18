package control

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/fredrik/shed/internal/httpgate"
	"github.com/fredrik/shed/internal/vm/vmspec"
)

func cmdShare(deps Deps) *cobra.Command {
	c := &cobra.Command{
		Use:   "share <vm>",
		Short: "Share a vm's HTTP front door",
		Long:  "Without a subcommand, prints a signed link that grants the visiting browser access to this private vm.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if _, ok := deps.Mgr.Get(name); !ok {
				return fmt.Errorf("no such vm %q", name)
			}
			fmt.Fprintln(cmd.OutOrStdout(), deps.Gate.URLWithToken(name))
			return nil
		},
	}

	c.AddCommand(&cobra.Command{
		Use:   "set-public <vm>",
		Short: "Make the vm's URL public (no token needed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := deps.Mgr.UpdateShare(args[0], func(sh *vmspec.Share) { sh.Public = true })
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "vm %s is now public: %s\n", rec.Spec.Name, deps.Gate.URL(rec.Spec.Name))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "set-private <vm>",
		Short: "Make the vm's URL private again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := deps.Mgr.UpdateShare(args[0], func(sh *vmspec.Share) { sh.Public = false })
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "vm %s is now private; share a link: ssh shed share %s\n", rec.Spec.Name, rec.Spec.Name)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "port <vm> <port>",
		Short: "Set which vm port the front door forwards to",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid port %q", args[1])
			}
			rec, err := deps.Mgr.UpdateShare(args[0], func(sh *vmspec.Share) { sh.Port = port })
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s now forwards to vm port %d\n", deps.Gate.URL(rec.Spec.Name), port)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "add <vm> <email>",
		Short: "Record a collaborator (single-user parity command)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := deps.Mgr.UpdateShare(args[0], func(sh *vmspec.Share) {
				for _, e := range sh.Emails {
					if e == args[1] {
						return
					}
				}
				sh.Emails = append(sh.Emails, args[1])
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s; on local shed everyone with the link is let in — send them: ssh shed share %s\n", args[1], args[0])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "ls <vm>",
		Short: "Show a vm's sharing state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, ok := deps.Mgr.Get(args[0])
			if !ok {
				return fmt.Errorf("no such vm %q", args[0])
			}
			visibility := "private"
			if rec.Share.Public {
				visibility = "public"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s, forwards to port %d\n", rec.Spec.Name, visibility, httpgate.TargetPort(rec))
			for _, e := range rec.Share.Emails {
				fmt.Fprintf(cmd.OutOrStdout(), "  shared with %s\n", e)
			}
			return nil
		},
	})
	return c
}
