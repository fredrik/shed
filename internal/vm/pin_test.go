package vm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fredrik/shed/internal/vm/vmspec"
)

// writeBase drops an empty stand-in base disk into the manager's cache.
func writeBase(t *testing.T, cacheDir, name string) string {
	t.Helper()
	dir := filepath.Join(cacheDir, "base")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A VM boots from the base disk it was created on, even after the image
// reference has moved on to a different digest.
func TestStartUsesPinnedBase(t *testing.T) {
	mgr, be := newTestManager(t)
	prep := mgr.prep.(*fakePrep)
	ctx := context.Background()
	pinned := writeBase(t, mgr.cfg.CacheDir, "fake.img")

	if _, err := mgr.Create(ctx, CreateOpts{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if got := be.LastStart().BaseDiskPath; got != pinned {
		t.Fatalf("first start base = %q, want pinned %q", got, pinned)
	}
	if err := mgr.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	// Upstream tag moved.
	prep.digest, prep.path = "sha256:moved", "/dev/zero"
	calls := prep.calls
	if err := mgr.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got := be.LastStart().BaseDiskPath; got != pinned {
		t.Fatalf("restart base = %q, want pinned %q", got, pinned)
	}
	if prep.calls != calls {
		t.Fatal("start re-resolved the image although the pinned base exists")
	}
	if rec, _ := mgr.Get("a"); rec.Image.Digest != "sha256:fake" {
		t.Fatalf("record digest drifted to %q", rec.Image.Digest)
	}
}

// If the pinned base is gone (cache wiped, or pruned by an older daemon),
// the VM moves to whatever the reference resolves to now, and the record
// says so.
func TestStartFallsBackWhenPinnedBaseGone(t *testing.T) {
	mgr, be := newTestManager(t)
	prep := mgr.prep.(*fakePrep)
	ctx := context.Background()

	if _, err := mgr.Create(ctx, CreateOpts{Name: "a", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	prep.digest, prep.path = "sha256:moved", "/dev/zero"
	if err := mgr.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got := be.LastStart().BaseDiskPath; got != "/dev/zero" {
		t.Fatalf("base = %q, want re-resolved /dev/zero", got)
	}
	if rec, _ := mgr.Get("a"); rec.Image.Digest != "sha256:moved" {
		t.Fatalf("record digest = %q, want sha256:moved", rec.Image.Digest)
	}
	recs, err := mgr.st.LoadVMs()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Image.Digest != "sha256:moved" {
		t.Fatalf("persisted record not updated: %+v", recs)
	}
}

// A sheduntu VM keeps its bake: no rebake is attempted while the pinned
// image is on disk, whatever the current recipe hashes to.
func TestStartUsesPinnedSheduntuBase(t *testing.T) {
	mgr, be := newTestManager(t)
	prep := mgr.prep.(*fakePrep)
	ctx := context.Background()
	pinned := writeBase(t, mgr.cfg.CacheDir, "sheduntu-abcdefabcdef.img")

	rec := &vmspec.VM{
		Spec:  vmspec.Spec{Name: "s", Image: "sheduntu", CPUs: 1, MemoryMB: 256, DiskGB: 1},
		Image: vmspec.ImageInfo{Digest: "sheduntu:abcdefabcdef", Cmd: []string{"/bin/bash"}},
		State: vmspec.StateStopped,
	}
	if err := mgr.st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Recover(); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Start(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if got := be.LastStart().BaseDiskPath; got != pinned {
		t.Fatalf("base = %q, want pinned %q", got, pinned)
	}
	if prep.calls != 0 {
		t.Fatal("a bake was attempted (EnsureImage called for the upstream base)")
	}
	if got := be.LastStart().GuestConfig.Cmd; !reflect.DeepEqual(got, []string{"/bin/bash"}) {
		t.Fatalf("guest cmd = %v, want the recorded one", got)
	}
}

func TestReferencedSheduntuTags(t *testing.T) {
	mgr, _ := newTestManager(t)
	for _, rec := range []*vmspec.VM{
		{Spec: vmspec.Spec{Name: "a", Image: "sheduntu", CPUs: 1, MemoryMB: 256, DiskGB: 1}, Image: vmspec.ImageInfo{Digest: "sheduntu:aaaaaaaaaaaa"}, State: vmspec.StateStopped},
		{Spec: vmspec.Spec{Name: "b", Image: "sheduntu", CPUs: 1, MemoryMB: 256, DiskGB: 1}, Image: vmspec.ImageInfo{Digest: "sheduntu:bbbbbbbbbbbb"}, State: vmspec.StateStopped},
		{Spec: vmspec.Spec{Name: "c", Image: "sheduntu", CPUs: 1, MemoryMB: 256, DiskGB: 1}, Image: vmspec.ImageInfo{Digest: "sheduntu:aaaaaaaaaaaa"}, State: vmspec.StateStopped},
		{Spec: vmspec.Spec{Name: "o", Image: "ubuntu:26.04", CPUs: 1, MemoryMB: 256, DiskGB: 1}, Image: vmspec.ImageInfo{Digest: "sha256:0123"}, State: vmspec.StateStopped},
	} {
		if err := mgr.st.SaveVM(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := mgr.Recover(); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"aaaaaaaaaaaa": true, "bbbbbbbbbbbb": true}
	if got := mgr.referencedSheduntuTags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
}
