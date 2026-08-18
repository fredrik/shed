//go:build linux

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// bootFromDisks assembles the VM root: /dev/vda is the read-only ext4 base
// built from the OCI image, /dev/vdb the writable per-VM ext4 data disk.
// They meet in an overlayfs that becomes the root via switch_root.
func bootFromDisks() error {
	fmt.Println("exeguest: assembling root from vda (base) + vdb (data)")

	if err := unix.Mount("/dev/vda", "/lower", "ext4", unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount base: %w", err)
	}
	if err := unix.Mount("/dev/vdb", "/data", "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount data: %w", err)
	}
	for _, dir := range []string{"/data/upper", "/data/work"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	opts := "lowerdir=/lower,upperdir=/data/upper,workdir=/data/work"
	if err := unix.Mount("overlay", "/newroot", "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}

	// Keep the agent reachable after the root switches away from the
	// initramfs.
	if err := copySelf("/newroot/.exe/agent"); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}

	// Move the virtual filesystems and the disk mounts into the new root.
	for _, dir := range []string{"/newroot/proc", "/newroot/sys", "/newroot/dev", "/newroot/.exe/lower", "/newroot/.exe/data"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	moves := [][2]string{
		{"/proc", "/newroot/proc"},
		{"/sys", "/newroot/sys"},
		{"/dev", "/newroot/dev"},
		{"/lower", "/newroot/.exe/lower"},
		{"/data", "/newroot/.exe/data"},
	}
	for _, m := range moves {
		if err := unix.Mount(m[0], m[1], "", unix.MS_MOVE, ""); err != nil {
			return fmt.Errorf("move %s: %w", m[0], err)
		}
	}

	// switch_root: make /newroot the root filesystem.
	if err := unix.Chdir("/newroot"); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}

	return runStage2()
}

func copySelf(dest string) error {
	if err := os.MkdirAll("/newroot/.exe", 0o755); err != nil {
		return err
	}
	src, err := os.Open("/init")
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := dst.ReadFrom(src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}
