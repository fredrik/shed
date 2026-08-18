package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/fredrik/local-devexe/internal/backend/vzbackend"
	"github.com/fredrik/local-devexe/internal/config"
	"github.com/fredrik/local-devexe/internal/control"
	"github.com/fredrik/local-devexe/internal/httpgate"
	"github.com/fredrik/local-devexe/internal/initramfs"
	"github.com/fredrik/local-devexe/internal/kernel"
	"github.com/fredrik/local-devexe/internal/keys"
	"github.com/fredrik/local-devexe/internal/sshgate"
	"github.com/fredrik/local-devexe/internal/store"
	"github.com/fredrik/local-devexe/internal/vm"
)

func cmdServe() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon (foreground)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serve()
		},
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.StateDir)
	if err != nil {
		return err
	}
	defer st.Close()

	hostSigner, err := keys.EnsureED25519(filepath.Join(cfg.StateDir, "keys", "host_ed25519"), "devexe-host")
	if err != nil {
		return err
	}
	brokerSigner, err := keys.EnsureED25519(filepath.Join(cfg.StateDir, "keys", "broker_ed25519"), "devexe-broker")
	if err != nil {
		return err
	}
	authorizedKeysPath := filepath.Join(cfg.StateDir, "authorized_keys")
	if n, err := keys.Seed(authorizedKeysPath); err != nil {
		log.Printf("seed authorized_keys: %v", err)
	} else if n == 0 {
		log.Printf("warning: no authorized keys (add one: ssh-keygen, then exed install)")
	}

	log.Printf("ensuring guest kernel (kata %s)...", kernel.Version)
	kernelPath, err := kernel.Ensure(cfg.CacheDir)
	if err != nil {
		return fmt.Errorf("kernel: %w", err)
	}

	initrdPath := filepath.Join(cfg.StateDir, "initramfs.cpio")
	if err := initramfs.WriteTo(initrdPath); err != nil {
		return fmt.Errorf("initramfs: %w", err)
	}

	be := vzbackend.New(kernelPath, initrdPath)
	prep := &vm.OCIPreparer{CacheDir: cfg.CacheDir}

	brokerPub := keys.AuthorizedLine(brokerSigner.PublicKey(), "devexe-broker")
	guestKeys := func() []string {
		lines, err := keys.AuthorizedLines(authorizedKeysPath)
		if err != nil {
			log.Printf("read authorized_keys for guest: %v", err)
		}
		return append(lines, brokerPub)
	}

	mgr := vm.NewManager(cfg, st, be, prep, kernelPath, guestKeys)
	if err := mgr.Recover(); err != nil {
		return fmt.Errorf("recover state: %w", err)
	}

	httpGate := &httpgate.Server{
		Addr:   cfg.HTTPAddr,
		Suffix: "exe.localhost",
		Mgr:    mgr,
	}
	if err := httpGate.EnsureSecret(filepath.Join(cfg.StateDir, "keys", "share_secret")); err != nil {
		return fmt.Errorf("share secret: %w", err)
	}

	me := "you"
	if u, err := user.Current(); err == nil {
		me = u.Username
	}
	gate := &sshgate.Server{
		Addr:               cfg.SSHAddr,
		HostSigner:         hostSigner,
		BrokerSigner:       brokerSigner,
		AuthorizedKeysPath: authorizedKeysPath,
		Mgr:                mgr,
		ControlDeps: control.Deps{
			Mgr:                mgr,
			Cfg:                cfg,
			Gate:               httpGate,
			AuthorizedKeysPath: authorizedKeysPath,
			User:               me,
		},
	}

	errCh := make(chan error, 2)
	go func() { errCh <- gate.ListenAndServe() }()
	go func() { errCh <- httpGate.ListenAndServe() }()
	go mgr.AutostartAll(context.Background())

	log.Printf("devexe ready: ssh exe@devexe (or ssh -p 2222 exe@127.0.0.1)")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("received %s, stopping vms...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		mgr.StopAll(ctx)
		gate.Close()
		return nil
	case err := <-errCh:
		return err
	}
}
