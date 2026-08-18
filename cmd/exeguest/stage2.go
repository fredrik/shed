//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// runStage2 runs after switch_root: fetch config from the host, bring the
// system up, and serve until shutdown.
func runStage2() error {
	printOSRelease()
	mountRuntimeFilesystems()

	ctl, err := dialControl()
	if err != nil {
		return fmt.Errorf("control: %w", err)
	}
	cfg, err := ctl.hello()
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	if cfg.Hostname != "" {
		unix.Sethostname([]byte(cfg.Hostname))
		os.WriteFile("/etc/hostname", []byte(cfg.Hostname+"\n"), 0o644)
		// sudo and friends resolve the hostname; make sure it resolves.
		os.WriteFile("/etc/hosts", []byte(
			"127.0.0.1\tlocalhost\n127.0.1.1\t"+cfg.Hostname+"\n::1\tlocalhost\n"), 0o644)
	}

	ip, err := networkUp(context.Background())
	if err != nil {
		ctl.reportError(err)
		return fmt.Errorf("network: %w", err)
	}

	if err := startSSHD(cfg.AuthorizedKeys, cfg.User); err != nil {
		ctl.reportError(err)
		return fmt.Errorf("sshd: %w", err)
	}
	startForwarder()

	if cfg.BakeScript != "" {
		// Bake mode: provision, then serve the rootfs tar; ready means
		// "baked and ready to harvest".
		if err := runBakeScript(cfg.BakeScript); err != nil {
			ctl.reportError(err)
			return err
		}
		if err := startBakeServer(); err != nil {
			ctl.reportError(err)
			return fmt.Errorf("bake server: %w", err)
		}
	} else {
		startWorkload(cfg)
	}

	if err := ctl.ready(ip.String(), cfg.Hostname); err != nil {
		return fmt.Errorf("report ready: %w", err)
	}
	fmt.Println("exeguest: ready")
	return ctl.waitShutdown()
}

// mountRuntimeFilesystems adds the mounts interactive sessions and services
// expect beyond what stage 1 set up: pseudo-terminals and /dev/shm.
func mountRuntimeFilesystems() {
	os.MkdirAll("/dev/pts", 0o755)
	if err := unix.Mount("devpts", "/dev/pts", "devpts", 0, "gid=5,mode=620,ptmxmode=666"); err != nil {
		fmt.Printf("exeguest: mount devpts: %v\n", err)
	}
	os.MkdirAll("/dev/shm", 0o1777)
	if err := unix.Mount("tmpfs", "/dev/shm", "tmpfs", 0, "mode=1777"); err != nil {
		fmt.Printf("exeguest: mount /dev/shm: %v\n", err)
	}
	os.MkdirAll("/run", 0o755)
	unix.Mount("tmpfs", "/run", "tmpfs", 0, "mode=755")
	os.MkdirAll("/tmp", 0o1777)
}

func printOSRelease() {
	cmd := exec.Command("/bin/sh", "-c", `. /etc/os-release 2>/dev/null; echo "exeguest: root is ${PRETTY_NAME:-unknown}"`)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Run()
}
