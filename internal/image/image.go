// Package image pulls OCI images and flattens them to rootfs tar streams.
// The rootfs is never extracted onto the host filesystem: it flows as a tar
// stream straight into an ext4 writer, preserving ownership, modes, and
// device nodes without root privileges on macOS.
package image

import (
	"context"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type Image struct {
	Ref string
	img v1.Image
}

// Pull fetches the linux/arm64 variant of ref from its registry.
// Credentials come from the default keychain (~/.docker/config.json) when
// present; anonymous pulls work for public images.
func Pull(ctx context.Context, ref string) (*Image, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse image ref %q: %w", ref, err)
	}
	img, err := remote.Image(r,
		remote.WithContext(ctx),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "arm64"}),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", ref, err)
	}
	return &Image{Ref: r.String(), img: img}, nil
}

// Digest returns the image's content digest, the cache key for base disks.
func (i *Image) Digest() (string, error) {
	d, err := i.img.Digest()
	if err != nil {
		return "", err
	}
	return d.String(), nil
}

// Flatten returns the image's filesystem as a single tar stream with all
// layers merged and whiteouts applied.
func (i *Image) Flatten() io.ReadCloser {
	return mutate.Extract(i.img)
}

// Config returns the OCI image config (entrypoint, cmd, env, exposed ports).
func (i *Image) Config() (*v1.ConfigFile, error) {
	return i.img.ConfigFile()
}
