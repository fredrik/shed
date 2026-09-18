// Package kernel fetches and caches shed's guest kernel: a monolithic
// arm64 Linux built from the recipe in the kernel/ directory of this
// repository and published as a release asset. It is the Kata Containers
// kernel configuration rebuilt from source — virtio blk/net/console/vsock,
// ext4, overlayfs, virtiofs, erofs and 9p built in, no modules — as an
// uncompressed Image, which Virtualization.framework's Linux boot loader
// requires on arm64.
//
// Set SHED_KERNEL to boot an Image built elsewhere instead — see Ensure.
package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const (
	// Version names the release tag (kernel-<Version>) the Image is
	// fetched from. A recipe change without an upstream bump still needs
	// a new tag; pick a new suffix rather than moving this one.
	Version = "6.18.15-shed"
	url     = "https://github.com/fredrik/shed/releases/download/kernel-" + Version + "/Image"
	// SHA-256 of the Image asset, as printed by `make kernel`.
	imageSHA256 = "375e2eb2e7468c946be1dcfb5e7e1707aba69ab10bc431a13648ddb41a34f415"
)

// Ensure returns the path to the verified kernel Image, downloading it on
// first use (about 16 MB) and re-hashing the cached copy on every call.
//
// SHED_KERNEL overrides all of that with a path to an Image of your own —
// one you just built with `make kernel`, say. It is taken on trust: the
// pin only describes the published asset, and a fresh build has no hash
// to check against. A SHED_KERNEL that does not exist is an error rather
// than a fallback, so a typo cannot masquerade as a kernel that booted.
func Ensure(cacheDir string) (string, error) {
	if path := os.Getenv("SHED_KERNEL"); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("SHED_KERNEL: %w", err)
		}
		return path, nil
	}
	return ensure(filepath.Join(cacheDir, "kernel", Version, "Image"), url, imageSHA256)
}

// ensure is Ensure with the pin made explicit, so tests can point it at a
// server of their own.
func ensure(dest, url, sha string) (string, error) {
	if ok, _ := verify(dest, sha); ok {
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
		return "", fmt.Errorf("download kernel: %s: %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".kernel-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed into place
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if ok, sum := verify(tmp.Name(), sha); !ok {
		return "", fmt.Errorf("kernel checksum mismatch: got %s want %s", sum, sha)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// verify reports whether the file at path hashes to want, and the hash it
// actually has (empty if the file could not be read).
func verify(path, want string) (bool, string) {
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
	return sum == want, sum
}
