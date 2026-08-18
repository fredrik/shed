package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fredrik/shed/internal/backend/stubbackend"
	"github.com/fredrik/shed/internal/config"
	"github.com/fredrik/shed/internal/store"
	"github.com/fredrik/shed/internal/vm/vmspec"
)

type fakePrep struct{}

func (fakePrep) EnsureImage(ctx context.Context, ref string) (vmspec.ImageInfo, string, error) {
	return vmspec.ImageInfo{Digest: "sha256:fake", ExposedPorts: []int{80}}, "/dev/null", nil
}

func (fakePrep) EnsureDataDisk(path string, sizeGB int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("disk"), 0o644)
}

func newTestManager(t *testing.T) (*Manager, *stubbackend.Backend) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg := &config.Config{
		DefaultImage: "test:latest", DefaultCPUs: 2, DefaultMemoryMB: 512, DefaultDiskGB: 5,
		Pool: config.Pool{CPUs: 4, MemoryMB: 2048, DiskGB: 20},
	}
	be := stubbackend.New()
	mgr := NewManager(cfg, st, be, fakePrep{}, "/no/kernel", func() []string { return []string{"ssh-ed25519 AAAA test"} })
	if err := mgr.Recover(); err != nil {
		t.Fatal(err)
	}
	return mgr, be
}

func TestCreateStartStopRemove(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	rec, err := mgr.Create(ctx, CreateOpts{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != vmspec.StateRunning {
		t.Fatalf("state = %s, want running", rec.State)
	}
	if rec.Spec.CPUs != 2 || rec.Spec.DiskGB != 5 {
		t.Fatalf("defaults not applied: %+v", rec.Spec)
	}

	if err := mgr.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := mgr.Get("a"); rec.State != vmspec.StateStopped {
		t.Fatalf("state after stop = %s", rec.State)
	}

	if err := mgr.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Remove(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.Get("a"); ok {
		t.Fatal("vm still present after remove")
	}
}

func TestPoolExhaustion(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	if _, err := mgr.Create(ctx, CreateOpts{Name: "a", CPUs: 4, MemoryMB: 1024}); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.Create(ctx, CreateOpts{Name: "b", CPUs: 1, MemoryMB: 512})
	if err == nil || !strings.Contains(err.Error(), "pool exhausted") {
		t.Fatalf("want pool exhausted on start, got %v", err)
	}
	// b exists (created) but couldn't start; stopping a frees cpu.
	if err := mgr.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	// Disk is held even while stopped.
	if _, err := mgr.Create(ctx, CreateOpts{Name: "c", DiskGB: 15}); err == nil || !strings.Contains(err.Error(), "disk") {
		t.Fatalf("want disk pool exhaustion, got %v", err)
	}
}

func TestGuestPowerOffSettlesState(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, CreateOpts{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	run, ok := mgr.Running("a")
	if !ok {
		t.Fatal("not running")
	}
	run.(*stubbackend.VM).StopFromGuest()
	<-run.Done()
	// watch() settles asynchronously; poll briefly.
	for i := 0; i < 100; i++ {
		if rec, _ := mgr.Get("a"); rec.State == vmspec.StateStopped {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, _ := mgr.Get("a")
	if rec.State != vmspec.StateStopped || rec.LastStopReason != "guest powered off" {
		t.Fatalf("state=%s reason=%q", rec.State, rec.LastStopReason)
	}
}

func TestStartFailureSetsError(t *testing.T) {
	mgr, be := newTestManager(t)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, CreateOpts{Name: "a", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	be.FailStart = errors.New("boom")
	if err := mgr.Start(ctx, "a"); err == nil {
		t.Fatal("want start error")
	}
	rec, _ := mgr.Get("a")
	if rec.State != vmspec.StateError {
		t.Fatalf("state = %s, want error", rec.State)
	}
	// error state is restartable
	if err := mgr.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverDemotesRunning(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := &vmspec.VM{Spec: vmspec.Spec{Name: "a", Image: "x", CPUs: 1, MemoryMB: 256, DiskGB: 1}, State: vmspec.StateRunning, IP: "1.2.3.4"}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	cfg := &config.Config{Pool: config.Pool{CPUs: 4, MemoryMB: 2048, DiskGB: 20}}
	mgr := NewManager(cfg, st2, stubbackend.New(), fakePrep{}, "", func() []string { return nil })
	if err := mgr.Recover(); err != nil {
		t.Fatal(err)
	}
	got, _ := mgr.Get("a")
	if got.State != vmspec.StateStopped || got.LastStopReason != "daemon restart" || got.IP != "" {
		t.Fatalf("recover: %+v", got)
	}
}

func TestRenameAndDuplicates(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, CreateOpts{Name: "a", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, CreateOpts{Name: "a", NoStart: true}); err == nil {
		t.Fatal("want duplicate name error")
	}
	if err := mgr.Rename(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.Get("a"); ok {
		t.Fatal("old name still resolves")
	}
	rec, ok := mgr.Get("b")
	if !ok || rec.Spec.Name != "b" {
		t.Fatalf("rename lost record: %+v", rec)
	}
}
