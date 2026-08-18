package control

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/spf13/cobra"
	gossh "golang.org/x/crypto/ssh"

	"github.com/fredrik/shed/internal/keys"
	"github.com/fredrik/shed/internal/vm"
	"github.com/fredrik/shed/internal/vm/vmspec"
)

func newRoot(deps Deps) *cobra.Command {
	root := &cobra.Command{
		Use:           "shed",
		Short:         "shed — local microVMs over ssh (an exe.dev clone)",
		SilenceUsage:  true,
		SilenceErrors: false,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}
	root.AddCommand(
		cmdNew(deps),
		cmdLs(deps),
		cmdRm(deps),
		cmdStart(deps),
		cmdStop(deps),
		cmdRestart(deps),
		cmdWhoami(deps),
		cmdSSHKey(deps),
		cmdBrowser(deps),
		cmdDoc(deps),
		cmdShare(deps),
		cmdCp(deps),
		cmdRename(deps),
		stub("shelley", "shelley — exe.dev's web agent; not part of the local clone (try: ssh <vm>@shed, then run claude)"),
	)
	return root
}

func cmdNew(deps Deps) *cobra.Command {
	var (
		imageRef  string
		cpus      int
		memoryMB  int
		diskGB    int
		autostart bool
		noStart   bool
		asJSON    bool
	)
	c := &cobra.Command{
		Use:   "new [name]",
		Short: "Create a microVM (and start it)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			} else {
				name = generateName(deps.Mgr)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			fmt.Fprintf(cmd.OutOrStdout(), "creating %s from %s...\n", name, orDefault(imageRef, deps.Cfg.DefaultImage))
			rec, err := deps.Mgr.Create(ctx, vm.CreateOpts{
				Name: name, Image: imageRef,
				CPUs: cpus, MemoryMB: memoryMB, DiskGB: diskGB,
				Autostart: autostart, NoStart: noStart,
				Progress: cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), rec)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "vm %s is %s\n", rec.Spec.Name, rec.State)
			fmt.Fprintf(cmd.OutOrStdout(), "  ssh %s@shed\n", rec.Spec.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", deps.Gate.URL(rec.Spec.Name))
			return nil
		},
	}
	c.Flags().StringVar(&imageRef, "image", "", "OCI image (default "+deps.Cfg.DefaultImage+")")
	c.Flags().IntVar(&cpus, "cpu", 0, "vCPUs")
	c.Flags().IntVar(&memoryMB, "memory", 0, "memory in MB")
	c.Flags().IntVar(&diskGB, "disk", 0, "disk in GB")
	c.Flags().BoolVar(&autostart, "autostart", false, "start this vm when the daemon starts")
	c.Flags().BoolVar(&noStart, "no-start", false, "create without starting")
	c.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return c
}

func cmdLs(deps Deps) *cobra.Command {
	var long, asJSON bool
	c := &cobra.Command{
		Use:   "ls",
		Short: "List your microVMs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			vms := deps.Mgr.List()
			if asJSON {
				if vms == nil {
					vms = []vmspec.VM{}
				}
				return printJSON(cmd.OutOrStdout(), vms)
			}
			if len(vms) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no vms yet — create one: ssh shed new")
				return nil
			}
			headers := []string{"NAME", "IMAGE", "STATE", "URL"}
			if long {
				headers = append(headers, "CPU", "MEM", "DISK", "IP", "CREATED")
			}
			var rows [][]string
			for _, rec := range vms {
				row := []string{rec.Spec.Name, rec.Spec.Image, string(rec.State), deps.Gate.URL(rec.Spec.Name)}
				if long {
					row = append(row,
						fmt.Sprint(rec.Spec.CPUs),
						fmt.Sprintf("%dMB", rec.Spec.MemoryMB),
						fmt.Sprintf("%dGB", rec.Spec.DiskGB),
						orDefault(rec.IP, "-"),
						rec.Spec.Created.Local().Format("2006-01-02 15:04"),
					)
				}
				rows = append(rows, row)
			}
			table(cmd.OutOrStdout(), headers, rows)
			if long {
				cpus, mem, disk := deps.Mgr.PoolUsage()
				pool := deps.Mgr.Pool()
				fmt.Fprintf(cmd.OutOrStdout(), "pool: cpu %d/%d, memory %d/%d MB, disk %d/%d GB\n",
					cpus, pool.CPUs, mem, pool.MemoryMB, disk, pool.DiskGB)
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&long, "long", "l", false, "more columns + pool usage")
	c.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return c
}

func cmdRm(deps Deps) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "rm <name>...",
		Short: "Delete microVMs (stops them first); disks are removed",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range args {
				if err := deps.Mgr.Remove(cmd.Context(), name); err != nil {
					return err
				}
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "vm %s removed\n", name)
				}
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), map[string]any{"removed": args})
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return c
}

func lifecycleCmd(deps Deps, verb string, action func(context.Context, string) error) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <name>...",
		Short: strings.ToUpper(verb[:1]) + verb[1:] + " microVMs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			for _, name := range args {
				if err := action(ctx, name); err != nil {
					return err
				}
				rec, _ := deps.Mgr.Get(name)
				fmt.Fprintf(cmd.OutOrStdout(), "vm %s is %s\n", name, rec.State)
			}
			return nil
		},
	}
}

func cmdStart(deps Deps) *cobra.Command {
	return lifecycleCmd(deps, "start", deps.Mgr.Start)
}
func cmdStop(deps Deps) *cobra.Command {
	return lifecycleCmd(deps, "stop", deps.Mgr.Stop)
}
func cmdRestart(deps Deps) *cobra.Command {
	return lifecycleCmd(deps, "restart", deps.Mgr.Restart)
}

func cmdCp(deps Deps) *cobra.Command {
	var start bool
	c := &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "Clone a vm (copy-on-write, instant)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			rec, err := deps.Mgr.Clone(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			if start {
				if err := deps.Mgr.Start(ctx, rec.Spec.Name); err != nil {
					return fmt.Errorf("cloned, but start failed: %w", err)
				}
				rec, _ = deps.Mgr.Get(rec.Spec.Name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "vm %s is %s (cloned from %s)\n", rec.Spec.Name, rec.State, args[0])
			fmt.Fprintf(cmd.OutOrStdout(), "  ssh %s@shed\n", rec.Spec.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", deps.Gate.URL(rec.Spec.Name))
			return nil
		},
	}
	c.Flags().BoolVar(&start, "start", true, "start the clone")
	return c
}

func cmdRename(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a vm (must be stopped)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := deps.Mgr.Rename(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "vm %s is now %s (ssh %s@shed)\n", args[0], args[1], args[1])
			return nil
		},
	}
}

func cmdWhoami(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who you are",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pubs, _ := keys.LoadAuthorizedKeys(deps.AuthorizedKeysPath)
			fmt.Fprintf(cmd.OutOrStdout(), "%s (local shed, %d authorized keys)\n", deps.User, len(pubs))
			return nil
		},
	}
}

func cmdSSHKey(deps Deps) *cobra.Command {
	c := &cobra.Command{
		Use:   "ssh-key",
		Short: "Manage authorized ssh keys",
	}
	c.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List authorized keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pubs, err := keys.LoadAuthorizedKeys(deps.AuthorizedKeysPath)
			if err != nil {
				return err
			}
			for _, p := range pubs {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", p.Type(), gossh.FingerprintSHA256(p))
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "add <authorized-keys-line>",
		Short: "Authorize a public key (also delivered to new vm boots); - reads stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := args[0]
			if raw == "-" {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				raw = string(b)
			}
			line := strings.TrimSpace(raw)
			if _, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line)); err != nil {
				return fmt.Errorf("not a valid public key: %w", err)
			}
			return appendLine(deps.AuthorizedKeysPath, line)
		},
	})
	return c
}

func cmdBrowser(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "browser <name>",
		Short: "Print the vm's URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, ok := deps.Mgr.Get(args[0]); !ok {
				return fmt.Errorf("no such vm %q", args[0])
			}
			fmt.Fprintln(cmd.OutOrStdout(), deps.Gate.URL(args[0]))
			return nil
		},
	}
}

func cmdDoc(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "doc",
		Short: "How shed works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprint(cmd.OutOrStdout(), docText)
			return nil
		},
	}
}

const docText = `shed — local microVMs over ssh (an exe.dev clone)

Every vm is a real Linux microVM (Virtualization.framework) booted from an
OCI image in about a second. The disk persists; stopped vms cost nothing
but disk. Your plan is a pool of cpu/memory/disk shared by all vms.

The default image is exeuntu: Ubuntu 24.04 with common tools preinstalled,
baked locally on first use (any OCI image works via --image).

  ssh mybox@shed                     shell in as dev (passwordless sudo);
                                     creates the vm first if it doesn't exist
  ssh shed new mybox                 create + start without a shell (exeuntu)
  ssh shed new web --image nginx     any OCI image works
  ssh shed ls -l                     fleet + pool usage
  ssh shed stop mybox                release cpu/memory
  ssh shed cp mybox mybox2           instant copy-on-write clone
  ssh shed rm mybox                  delete, including disk

HTTP: each vm is reachable at http://<name>.shed.localhost:8080 — private
by default; ssh shed share <name> prints an access link.

Locally, bin/shed runs the same commands over the daemon's unix socket —
no keys involved (shed ls, shed new mybox, cat k.pub | shed ssh-key add -).
Interactive shells still go over ssh.
`

func stub(name, msg string) *cobra.Command {
	return &cobra.Command{
		Use:    name,
		Short:  msg,
		Hidden: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s", msg)
		},
	}
}

var nameAdjectives = []string{"amber", "brisk", "calm", "dapper", "eager", "fuzzy", "gentle", "happy", "ivory", "jolly", "keen", "lively", "mellow", "nimble", "opal", "plucky", "quiet", "rustic", "sunny", "tidy"}
var nameNouns = []string{"otter", "falcon", "maple", "comet", "harbor", "meadow", "pebble", "spruce", "tundra", "willow", "badger", "cinder", "dune", "ember", "fjord", "grove", "heron", "islet", "jetty", "knoll"}

func generateName(mgr *vm.Manager) string {
	for {
		a := nameAdjectives[randInt(len(nameAdjectives))]
		n := nameNouns[randInt(len(nameNouns))]
		name := a + "-" + n
		if _, exists := mgr.Get(name); !exists {
			return name
		}
	}
}

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func appendLine(path, line string) error {
	return appendFile(path, line+"\n")
}
