// Package diskfs builds ext4 block-device images on macOS without root:
// read-only base disks via the pure-Go tar2ext4 writer, writable data disks
// via Homebrew's mke2fs (the project's only external tool dependency).
package diskfs

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
)

// mke2fs is keg-only in Homebrew, so it is not on PATH.
var mke2fsCandidates = []string{
	"/opt/homebrew/opt/e2fsprogs/sbin/mke2fs",
	"/usr/local/opt/e2fsprogs/sbin/mke2fs",
}

// BuildBaseDisk converts a flattened rootfs tar stream into a read-only
// ext4 image at dest. Ownership, modes, xattrs, and device nodes come from
// the tar headers, so no privileges are needed. The output carries ext4's
// read-only compat flag and is mounted ro as an overlay lowerdir.
func BuildBaseDisk(tarStream io.Reader, dest string, maxSize int64) (err error) {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dest)
		}
	}()
	if err := tar2ext4.Convert(tarStream, f, tar2ext4.MaximumDiskSize(maxSize)); err != nil {
		return fmt.Errorf("tar2ext4: %w", err)
	}
	return nil
}

// NewDataDisk creates a writable, sparse ext4 image of sizeBytes at dest.
// APFS keeps unwritten ranges as holes, so a 10 GB disk costs megabytes.
func NewDataDisk(dest string, sizeBytes int64) error {
	mke2fs, err := findMke2fs()
	if err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		os.Remove(dest)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	cmd := exec.Command(mke2fs, "-q", "-F", "-t", "ext4",
		"-E", "root_owner=0:0,lazy_itable_init=1,lazy_journal_init=1", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(dest)
		return fmt.Errorf("mke2fs: %w: %s", err, out)
	}
	return nil
}

func findMke2fs() (string, error) {
	for _, p := range mke2fsCandidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("mke2fs"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("mke2fs not found: install with `brew install e2fsprogs`")
}
