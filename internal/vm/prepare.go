package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fredrik/shed/internal/diskfs"
	"github.com/fredrik/shed/internal/image"
	"github.com/fredrik/shed/internal/vm/vmspec"
)

// OCIPreparer is the real Preparer: pulls images with go-containerregistry
// and builds base disks with tar2ext4, cached by digest in the cache dir.
type OCIPreparer struct {
	CacheDir string

	mu sync.Mutex // one build at a time; builds are seconds
}

func (p *OCIPreparer) EnsureImage(ctx context.Context, ref string) (vmspec.ImageInfo, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	img, err := image.Pull(ctx, ref)
	if err != nil {
		return vmspec.ImageInfo{}, "", err
	}
	digest, err := img.Digest()
	if err != nil {
		return vmspec.ImageInfo{}, "", err
	}
	cfgFile, err := img.Config()
	if err != nil {
		return vmspec.ImageInfo{}, "", err
	}

	info := vmspec.ImageInfo{
		Digest:     digest,
		Entrypoint: cfgFile.Config.Entrypoint,
		Cmd:        cfgFile.Config.Cmd,
		Env:        cfgFile.Config.Env,
		WorkingDir: cfgFile.Config.WorkingDir,
	}
	for portProto := range cfgFile.Config.ExposedPorts {
		var port int
		if _, err := fmt.Sscanf(portProto, "%d", &port); err == nil && port > 0 {
			if strings.Contains(portProto, "udp") {
				continue
			}
			info.ExposedPorts = append(info.ExposedPorts, port)
		}
	}
	sort.Ints(info.ExposedPorts)

	baseDir := filepath.Join(p.CacheDir, "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return vmspec.ImageInfo{}, "", err
	}
	basePath := filepath.Join(baseDir, strings.TrimPrefix(digest, "sha256:")+".img")
	if _, err := os.Stat(basePath); err != nil {
		tarStream := img.Flatten()
		defer tarStream.Close()
		if err := diskfs.BuildBaseDisk(tarStream, basePath, 16*1024*1024*1024); err != nil {
			return vmspec.ImageInfo{}, "", fmt.Errorf("build base disk: %w", err)
		}
	}
	return info, basePath, nil
}

func (p *OCIPreparer) EnsureDataDisk(path string, sizeGB int) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return diskfs.NewDataDisk(path, int64(sizeGB)*1024*1024*1024)
}
