package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fredrik/local-devexe/internal/config"
	"github.com/fredrik/local-devexe/internal/keys"
)

func cmdInstall() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Set up ssh config and authorized keys (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return install(cmd.OutOrStdout())
		},
	}
}

func install(out interface{ Write([]byte) (int, error) }) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "keys"), 0o700); err != nil {
		return err
	}

	authorizedKeysPath := filepath.Join(cfg.StateDir, "authorized_keys")
	n, err := keys.Seed(authorizedKeysPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "authorized_keys: %d key(s) at %s\n", n, authorizedKeysPath)
	if n == 0 {
		fmt.Fprintf(out, "  no ~/.ssh/*.pub found — create one: ssh-keygen -t ed25519\n")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(cfg.SSHAddr)
	if err != nil {
		return err
	}

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	devexeConfig := fmt.Sprintf(`# Written by exed install. The devexe host is the control plane
# (ssh devexe <command>); vms are reached as <vmname>@devexe.
Host devexe
  HostName %s
  Port %s
  User exe
  UserKnownHostsFile %s
  StrictHostKeyChecking accept-new
`, host, port, filepath.Join(cfg.StateDir, "known_hosts"))
	configPath := filepath.Join(sshDir, "devexe_config")
	if err := os.WriteFile(configPath, []byte(devexeConfig), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s\n", configPath)

	mainConfig := filepath.Join(sshDir, "config")
	includeLine := "Include devexe_config"
	data, err := os.ReadFile(mainConfig)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !strings.Contains(string(data), includeLine) {
		newData := includeLine + "\n\n" + string(data)
		if err := os.WriteFile(mainConfig, []byte(newData), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(out, "added %q to %s\n", includeLine, mainConfig)
	} else {
		fmt.Fprintf(out, "%s already includes devexe_config\n", mainConfig)
	}

	fmt.Fprintf(out, "\nnext:\n  make build && bin/exed serve   # run the daemon\n  ssh devexe new mybox            # create a vm\n  ssh mybox@devexe                # shell in\n")
	return nil
}
