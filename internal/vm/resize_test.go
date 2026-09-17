package vm

import (
	"context"
	"strings"
	"testing"
)

func TestResizeStoppedVM(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, CreateOpts{Name: "a", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Resize(ctx, "a", 3, 1536); err != nil {
		t.Fatal(err)
	}
	rec, _ := mgr.Get("a")
	if rec.Spec.CPUs != 3 || rec.Spec.MemoryMB != 1536 {
		t.Fatalf("spec not updated: %+v", rec.Spec)
	}
	// Zero keeps the current value.
	if err := mgr.Resize(ctx, "a", 0, 256); err != nil {
		t.Fatal(err)
	}
	rec, _ = mgr.Get("a")
	if rec.Spec.CPUs != 3 || rec.Spec.MemoryMB != 256 {
		t.Fatalf("zero cpu should keep 3: %+v", rec.Spec)
	}
	// The change is persisted.
	saved, err := mgr.st.LoadVMs()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Spec.CPUs != 3 || saved[0].Spec.MemoryMB != 256 {
		t.Fatalf("resize not saved: %+v", saved)
	}
}

func TestResizeRejects(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, CreateOpts{Name: "a", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		cpus  int
		mem   int
		wants string
	}{
		{"no change", 0, 0, "nothing to change"},
		{"below cpu floor", -1, 0, "cpus must be >= 1"},
		{"below memory floor", 0, 64, "memory must be >= 128 MB"},
		{"above pool cpus", 5, 0, "pool.cpus"},
		{"above pool memory", 0, 4096, "pool.memory_mb"},
	}
	for _, tc := range cases {
		err := mgr.Resize(ctx, "a", tc.cpus, tc.mem)
		if err == nil || !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.wants)
		}
	}
	rec, _ := mgr.Get("a")
	if rec.Spec.CPUs != 2 || rec.Spec.MemoryMB != 512 {
		t.Fatalf("rejected resize changed spec: %+v", rec.Spec)
	}
	if err := mgr.Resize(ctx, "nope", 1, 0); err == nil {
		t.Fatal("want no such vm error")
	}
}

func TestResizeRunningVMFails(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, CreateOpts{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	err := mgr.Resize(ctx, "a", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("got %v, want running error", err)
	}
}
