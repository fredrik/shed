package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fredrik/local-devexe/internal/backend"
	"github.com/fredrik/local-devexe/internal/diskfs"
	"github.com/fredrik/local-devexe/internal/vm/vmspec"
	"github.com/fredrik/local-devexe/internal/vsockproto"
)

// exeuntu is devexe's default image, in the spirit of exe.dev's: Ubuntu
// with the tools you expect already installed. It is baked locally, once:
// a throwaway VM boots the upstream Ubuntu image, runs the recipe below,
// and streams its merged rootfs back to become a cached base image.
// Upstream digest changes (Ubuntu security updates) or recipe edits change
// the cache key, so the image tracks upstream on next use.

const (
	exeuntuName    = "exeuntu"
	exeuntuBase    = "ubuntu:24.04"
	exeuntuVersion = "v1" // bump to force a rebake without editing the recipe

	exeuntuScript = `set -eux
apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates curl wget git vim nano less htop tmux \
  ripgrep jq unzip zip file rsync openssh-client sudo \
  iproute2 iputils-ping dnsutils netcat-openbsd
apt-get clean

# The default login user: uid 1000, bash, passwordless sudo. The stock
# ubuntu user makes way so uid 1000 stays conventional.
userdel -r ubuntu 2>/dev/null || true
useradd -m -u 1000 -s /bin/bash dev
usermod -aG sudo dev
echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev
chmod 440 /etc/sudoers.d/dev

cat > /etc/motd <<'MOTD'

  exeuntu -- Ubuntu 24.04, devexe build

  This microVM is yours: persistent disk, apt works, sudo is free.
  Its web port is proxied at http://<vmname>.exe.localhost:8080
  Manage the fleet: ssh devexe help

MOTD
`
)

func isExeuntu(ref string) bool {
	return ref == exeuntuName || ref == exeuntuName+":latest"
}

// ensureImage resolves any image reference, treating exeuntu specially.
func (m *Manager) ensureImage(ctx context.Context, ref string, progress io.Writer) (vmspec.ImageInfo, string, error) {
	if isExeuntu(ref) {
		return m.ensureExeuntu(ctx, progress)
	}
	return m.prep.EnsureImage(ctx, ref)
}

func (m *Manager) ensureExeuntu(ctx context.Context, progress io.Writer) (vmspec.ImageInfo, string, error) {
	baseInfo, basePath, err := m.prep.EnsureImage(ctx, exeuntuBase)
	if err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("exeuntu base %s: %w", exeuntuBase, err)
	}

	sum := sha256.Sum256([]byte(exeuntuVersion + "\x00" + baseInfo.Digest + "\x00" + exeuntuScript))
	tag := hex.EncodeToString(sum[:])[:12]
	imgPath := filepath.Join(m.cfg.CacheDir, "base", "exeuntu-"+tag+".img")
	infoPath := imgPath + ".json"

	if info, err := loadImageInfo(infoPath); err == nil {
		if _, err := os.Stat(imgPath); err == nil {
			return info, imgPath, nil
		}
	}

	fmt.Fprintf(progress, "baking the exeuntu image (first time only, a few minutes)...\n")

	bakeDir, err := os.MkdirTemp("", "devexe-bake-*")
	if err != nil {
		return vmspec.ImageInfo{}, "", err
	}
	defer os.RemoveAll(bakeDir)
	dataDisk := filepath.Join(bakeDir, "data.img")
	if err := m.prep.EnsureDataDisk(dataDisk, 8); err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("bake data disk: %w", err)
	}

	bctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	serialLog := filepath.Join(m.cfg.CacheDir, "exeuntu-bake.log")
	run, err := m.be.Start(bctx, backend.StartRequest{
		Spec: vmspec.Spec{
			Name: "exeuntu-bake", Image: exeuntuBase,
			CPUs: 2, MemoryMB: 2048, DiskGB: 8,
			Created: time.Now().UTC(),
		},
		BaseDiskPath:  basePath,
		DataDiskPath:  dataDisk,
		KernelPath:    m.kernel,
		SerialLogPath: serialLog,
		GuestConfig: vsockproto.Config{
			Hostname:       "exeuntu",
			AuthorizedKeys: m.guestKeys(),
			BakeScript:     exeuntuScript,
		},
	})
	if err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("bake vm: %w (serial log: %s)", err, serialLog)
	}
	defer run.Kill()

	conn, err := run.DialGuest(bctx, vsockproto.BakeTarPort)
	if err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("fetch baked rootfs: %w", err)
	}
	defer conn.Close()

	fmt.Fprintf(progress, "harvesting baked rootfs into base image...\n")
	if err := diskfs.BuildBaseDisk(conn, imgPath, 16*1024*1024*1024); err != nil {
		return vmspec.ImageInfo{}, "", fmt.Errorf("build exeuntu base: %w", err)
	}

	info := vmspec.ImageInfo{
		Digest: "exeuntu:" + tag,
		Env:    []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cmd:    []string{"/bin/bash"},
	}
	if err := saveImageInfo(infoPath, info); err != nil {
		os.Remove(imgPath)
		return vmspec.ImageInfo{}, "", err
	}

	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	run.Shutdown(shutdownCtx)
	return info, imgPath, nil
}

func loadImageInfo(path string) (vmspec.ImageInfo, error) {
	var info vmspec.ImageInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	return info, json.Unmarshal(data, &info)
}

func saveImageInfo(path string, info vmspec.ImageInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
