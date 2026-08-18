// Package kernel fetches and caches the pinned guest kernel: the Kata
// Containers static arm64 build — the same kernel Apple's `container`
// stack direct-boots with Virtualization.framework. Monolithic, with
// virtio blk/net/console/vsock, ext4, and overlayfs built in.
package kernel

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	Version = "3.28.0"
	url     = "https://github.com/kata-containers/kata-containers/releases/download/3.28.0/kata-static-3.28.0-arm64.tar.zst"
	// Member inside the release tarball (Apple's containerization pin).
	memberPath = "opt/kata/share/kata-containers/vmlinux-6.18.15-186"
	// SHA-256 of the extracted kernel Image.
	imageSHA256 = "2fe4a58d2885d623bcb4d705900ac8c1d4f02371152da8126b3b00c8c47fc3a1"
)

// Ensure returns the path to the verified kernel Image, downloading and
// extracting it on first use (~600 MB download, ~16 MB kept).
func Ensure(cacheDir string) (string, error) {
	dest := filepath.Join(cacheDir, "kernel", Version, "Image")
	if ok, _ := verify(dest); ok {
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download kernel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download kernel: %s", resp.Status)
	}

	zr, err := zstd.NewReader(resp.Body)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("kernel member %s not found in release tarball", memberPath)
		}
		if err != nil {
			return "", err
		}
		if strings.TrimPrefix(hdr.Name, "./") != memberPath || hdr.Typeflag != tar.TypeReg {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(dest), ".kernel-*")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return "", err
		}
		if ok, sum := verify(tmp.Name()); !ok {
			os.Remove(tmp.Name())
			return "", fmt.Errorf("kernel checksum mismatch: got %s want %s", sum, imageSHA256)
		}
		if err := os.Rename(tmp.Name(), dest); err != nil {
			os.Remove(tmp.Name())
			return "", err
		}
		return dest, nil
	}
}

func verify(path string) (bool, string) {
	f, err := os.Open(path)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, ""
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return sum == imageSHA256, sum
}
