package vm

import (
	"os"
	"path/filepath"
	"strings"
)

// A VM is pinned to the base disk it was created on: the digest recorded
// in vm.json names a file in the cache, and Start boots that file for as
// long as it exists. Re-resolving the image reference instead would swap
// the lower layer of an overlay whose upper layer was written against the
// old one — apt-upgraded libraries in the data disk shadowing a base whose
// binaries want a newer glibc, say.

// pinnedBaseDisk maps a recorded image digest to its cached base disk.
// ok is false when the digest is unknown or the file is gone.
func pinnedBaseDisk(cacheDir, digest string) (path string, ok bool) {
	var name string
	switch {
	case strings.HasPrefix(digest, sheduntuName+":"):
		name = sheduntuName + "-" + strings.TrimPrefix(digest, sheduntuName+":") + ".img"
	case strings.HasPrefix(digest, "sha256:"):
		name = strings.TrimPrefix(digest, "sha256:") + ".img"
	default:
		return "", false
	}
	path = filepath.Join(cacheDir, "base", name)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

// sheduntuTagOf recovers the bake tag from a cached image path,
// .../sheduntu-<tag>.img.
func sheduntuTagOf(imgPath string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(imgPath), sheduntuName+"-"), ".img")
}

// referencedSheduntuTags is the set of sheduntu bake tags some VM record
// still points at. Prune must keep these however old they are.
func (m *Manager) referencedSheduntuTags() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	tags := map[string]bool{}
	for _, e := range m.vms {
		if tag, ok := strings.CutPrefix(e.rec.Image.Digest, sheduntuName+":"); ok && tag != "" {
			tags[tag] = true
		}
	}
	return tags
}
