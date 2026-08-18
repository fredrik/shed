package vm

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/fredrik/shed/internal/vm/vmspec"
)

// Clone copies a VM: the data disk is an APFS clonefile (instant,
// copy-on-write), the base disk is shared by digest. A running source is
// quiesced for the copy and started again.
func (m *Manager) Clone(ctx context.Context, src, dst string) (vmspec.VM, error) {
	if err := vmspec.ValidName(dst); err != nil {
		return vmspec.VM{}, err
	}

	m.mu.Lock()
	se, ok := m.vms[src]
	if !ok {
		m.mu.Unlock()
		return vmspec.VM{}, fmt.Errorf("no such vm %q", src)
	}
	if _, exists := m.vms[dst]; exists {
		m.mu.Unlock()
		return vmspec.VM{}, fmt.Errorf("vm %q already exists", dst)
	}
	if se.busy != "" {
		m.mu.Unlock()
		return vmspec.VM{}, fmt.Errorf("vm %q is busy (%s)", src, se.busy)
	}
	if _, _, usedDisk := m.poolUsageLocked(); usedDisk+se.rec.Spec.DiskGB > m.cfg.Pool.DiskGB {
		m.mu.Unlock()
		return vmspec.VM{}, fmt.Errorf("pool exhausted: need %d GB disk, %d GB free", se.rec.Spec.DiskGB, m.cfg.Pool.DiskGB-usedDisk)
	}
	wasRunning := se.rec.State == vmspec.StateRunning
	srcRec := *se.rec
	m.mu.Unlock()

	// Quiesce: a live ext4 can't be cloned consistently.
	if wasRunning {
		if err := m.Stop(ctx, src); err != nil {
			return vmspec.VM{}, fmt.Errorf("quiesce %s: %w", src, err)
		}
	}

	newRec := &vmspec.VM{
		Spec: vmspec.Spec{
			Name:     dst,
			Image:    srcRec.Spec.Image,
			CPUs:     srcRec.Spec.CPUs,
			MemoryMB: srcRec.Spec.MemoryMB,
			DiskGB:   srcRec.Spec.DiskGB,
			Created:  time.Now().UTC(),
		},
		Image: srcRec.Image,
		Share: vmspec.Share{Port: srcRec.Share.Port}, // clones start private
		State: vmspec.StateStopped,
	}

	if err := os.MkdirAll(m.st.VMDir(dst), 0o755); err != nil {
		return vmspec.VM{}, err
	}
	if err := unix.Clonefile(m.dataDiskPath(src), m.dataDiskPath(dst), 0); err != nil {
		os.RemoveAll(m.st.VMDir(dst))
		return vmspec.VM{}, fmt.Errorf("clone data disk: %w", err)
	}
	if err := m.st.SaveVM(newRec); err != nil {
		os.RemoveAll(m.st.VMDir(dst))
		return vmspec.VM{}, err
	}

	m.mu.Lock()
	m.vms[dst] = &entry{rec: newRec}
	m.mu.Unlock()

	if wasRunning {
		if err := m.Start(ctx, src); err != nil {
			return *newRec, fmt.Errorf("cloned, but restarting %s failed: %w", src, err)
		}
	}
	return *newRec, nil
}

// Rename changes a VM's name (which is also its ssh username, hostname,
// and URL). The VM must be stopped: the name seeds its MAC and hostname.
func (m *Manager) Rename(ctx context.Context, oldName, newName string) error {
	if err := vmspec.ValidName(newName); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.vms[oldName]
	if !ok {
		return fmt.Errorf("no such vm %q", oldName)
	}
	if _, exists := m.vms[newName]; exists {
		return fmt.Errorf("vm %q already exists", newName)
	}
	if e.busy != "" {
		return fmt.Errorf("vm %q is busy (%s)", oldName, e.busy)
	}
	if e.rec.State == vmspec.StateRunning || e.rec.State == vmspec.StateStarting {
		return fmt.Errorf("vm %q is running — stop it first: ssh shed stop %s", oldName, oldName)
	}
	if err := os.Rename(m.st.VMDir(oldName), m.st.VMDir(newName)); err != nil {
		return err
	}
	e.rec.Spec.Name = newName
	delete(m.vms, oldName)
	m.vms[newName] = e
	return m.st.SaveVM(e.rec)
}
