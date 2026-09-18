package vm

import (
	"context"
	"fmt"

	"github.com/fredrik/shed/internal/vm/vmspec"
)

// Resize changes a stopped VM's cpu and memory allocation. Zero keeps the
// current value; at least one must be non-zero. The new shape must pass
// the backend's floors and fit under the pool ceilings on its own, since
// a VM larger than the pool could never start. Free capacity is not
// checked here: stopped VMs don't count against cpu or memory, and Start
// enforces the pool when the VM next boots.
func (m *Manager) Resize(ctx context.Context, name string, cpus, memoryMB int) error {
	if cpus == 0 && memoryMB == 0 {
		return fmt.Errorf("nothing to change: pass --cpu and/or --memory")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.vms[name]
	if !ok {
		return fmt.Errorf("no such vm %q", name)
	}
	if e.busy != "" {
		return fmt.Errorf("vm %q is busy (%s)", name, e.busy)
	}
	if e.rec.State == vmspec.StateRunning || e.rec.State == vmspec.StateStarting {
		return fmt.Errorf("vm %q is running — stop it first: ssh shed stop %s", name, name)
	}
	spec := e.rec.Spec
	if cpus != 0 {
		spec.CPUs = cpus
	}
	if memoryMB != 0 {
		spec.MemoryMB = memoryMB
	}
	if err := m.be.Validate(spec); err != nil {
		return err
	}
	if spec.CPUs > m.cfg.Pool.CPUs {
		return fmt.Errorf("%d cpus exceeds the pool of %d (raise pool.cpus)", spec.CPUs, m.cfg.Pool.CPUs)
	}
	if spec.MemoryMB > m.cfg.Pool.MemoryMB {
		return fmt.Errorf("%d MB memory exceeds the pool of %d MB (raise pool.memory_mb)", spec.MemoryMB, m.cfg.Pool.MemoryMB)
	}
	e.rec.Spec = spec
	return m.st.SaveVM(e.rec)
}
