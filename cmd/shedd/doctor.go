package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/fredrik/shed/internal/config"
)

func cmdDoctor() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine can run shed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doctor()
		},
	}
}

func doctor() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ok := true
	check := func(name string, pass bool, hint string) {
		status := "ok"
		if !pass {
			status = "FAIL"
			ok = false
		}
		fmt.Printf("%-38s %s\n", name, status)
		if !pass && hint != "" {
			fmt.Printf("  → %s\n", hint)
		}
	}

	exe, _ := os.Executable()
	signed := false
	if exe != "" {
		out, err := exec.Command("codesign", "-d", "--entitlements", "-", "--xml", exe).CombinedOutput()
		signed = err == nil && containsBytes(out, []byte("com.apple.security.virtualization"))
	}
	check("binary signed with virtualization", signed, "build with `make build`, not `go build`")

	mke2fsFound := false
	for _, p := range []string{"/opt/homebrew/opt/e2fsprogs/sbin/mke2fs", "/usr/local/opt/e2fsprogs/sbin/mke2fs"} {
		if _, err := os.Stat(p); err == nil {
			mke2fsFound = true
		}
	}
	check("e2fsprogs (mke2fs)", mke2fsFound, "brew install e2fsprogs")

	kernelPath := filepath.Join(cfg.CacheDir, "kernel")
	_, kerr := os.Stat(kernelPath)
	check("guest kernel cached", kerr == nil, "downloaded on first `shedd serve` (~600 MB)")

	for _, addr := range []string{cfg.SSHAddr, cfg.HTTPAddr} {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
		}
		check("port free: "+addr, err == nil, "another process (or shedd) is listening")
	}

	if !ok {
		return fmt.Errorf("some checks failed")
	}
	return nil
}

func containsBytes(haystack, needle []byte) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && searchBytes(haystack, needle))
}

func searchBytes(h, n []byte) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if string(h[i:i+len(n)]) == string(n) {
			return true
		}
	}
	return false
}
