//go:build linux

package main

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fredrik/shed/internal/vsockproto"
)

// runBakeScript provisions the image-to-be. Output goes to the console so
// the serial log tells the story if it fails.
func runBakeScript(script string) error {
	fmt.Println("shedguest: bake script starting")
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"DEBIAN_FRONTEND=noninteractive",
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bake script: %w", err)
	}
	fmt.Println("shedguest: bake script done")
	return nil
}

// startBakeServer serves one tar of the merged rootfs on guest loopback,
// where the host's DialGuest can fetch it.
func startBakeServer() error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", vsockproto.BakeTarPort))
	if err != nil {
		return err
	}
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if err := writeRootTar(conn); err != nil {
			fmt.Printf("shedguest: bake tar: %v\n", err)
			return
		}
		fmt.Println("shedguest: bake tar sent")
	}()
	return nil
}

// bakeSkip are paths that must not be captured into the image: virtual
// filesystems, the agent's plumbing, and per-boot files the agent rewrites
// (hostname, resolv.conf, authorized keys).
var bakeSkip = map[string]bool{
	"/proc":                      true,
	"/sys":                       true,
	"/dev":                       true,
	"/run":                       true,
	"/tmp":                       true,
	"/.shed":                      true,
	"/lost+found":                true,
	"/etc/hostname":              true,
	"/etc/resolv.conf":           true,
	"/root/.ssh/authorized_keys": true,
	// apt metadata: big and stale by the time the image is used
	"/var/lib/apt/lists": true,
	"/var/cache/apt":     true,
}

// writeRootTar tars the merged root in parent-before-child order (which
// tar2ext4 on the host requires), preserving ownership and modes.
// Hardlinks are detected by inode so dpkg-managed trees don't bloat.
func writeRootTar(w io.Writer) error {
	tw := tar.NewWriter(w)
	seen := map[uint64]string{} // inode → first path, for hardlinks

	// Mount points excluded from the walk still need to exist as
	// directories in the image.
	for _, dir := range []string{"/dev", "/proc", "/run", "/sys", "/tmp"} {
		hdr := &tar.Header{Name: strings.TrimPrefix(dir, "/") + "/", Typeflag: tar.TypeDir, Mode: 0o755}
		if dir == "/tmp" {
			hdr.Mode = 0o1777
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
	}

	err := filepath.WalkDir("/", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, never fail the bake
		}
		if path == "/" {
			return nil
		}
		if bakeSkip[path] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, _ := info.Sys().(*syscall.Stat_t)

		var linkTarget string
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return nil
			}
		}
		hdr, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return nil
		}
		hdr.Name = strings.TrimPrefix(path, "/")
		if d.IsDir() {
			hdr.Name += "/"
		}
		if st != nil {
			hdr.Uid = int(st.Uid)
			hdr.Gid = int(st.Gid)
			if info.Mode().IsRegular() && st.Nlink > 1 {
				if first, ok := seen[st.Ino]; ok {
					hdr.Typeflag = tar.TypeLink
					hdr.Linkname = first
					hdr.Size = 0
				} else {
					seen[st.Ino] = hdr.Name
				}
			}
		}
		if info.Mode()&(os.ModeSocket|os.ModeNamedPipe) != 0 {
			return nil // sockets/fifos are runtime state
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 {
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			_, err = io.CopyN(tw, f, hdr.Size)
			f.Close()
			if err != nil {
				return fmt.Errorf("copy %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}
