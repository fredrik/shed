// Package initramfs builds the tiny initramfs that boots every devexe VM:
// an uncompressed newc cpio archive whose only contents are the exeguest
// agent as /init plus the mount points it needs.
package initramfs

import (
	_ "embed"
	"io"
	"os"
	"path/filepath"
)

// Built by `make agent` (GOOS=linux GOARCH=arm64 CGO_ENABLED=0); gitignored.
//
//go:embed exeguest_linux_arm64
var agentBinary []byte

// Build writes the initramfs archive. Uncompressed: it avoids depending on
// the kernel's RD_* decompressors and the agent is small.
func Build(w io.Writer) error {
	cw := &cpioWriter{w: w}
	for _, dir := range []string{"dev", "proc", "sys", "newroot", "lower", "data"} {
		if err := cw.Dir(dir, 0o755); err != nil {
			return err
		}
	}
	// /dev/console (c 5 1) so the kernel can wire up pid1's stdio even
	// before devtmpfs is mounted.
	if err := cw.CharDev("dev/console", 0o600, 5, 1); err != nil {
		return err
	}
	if err := cw.File("init", 0o755, agentBinary); err != nil {
		return err
	}
	return cw.Trailer()
}

// WriteTo writes the initramfs to path atomically.
func WriteTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".initramfs-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := Build(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
