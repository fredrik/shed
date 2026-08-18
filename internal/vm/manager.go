// Package vm is the manager: it owns the VM registry, drives lifecycle
// transitions through the backend, accounts the resource pool, and keeps
// the store truthful across daemon restarts.
package vm

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fredrik/local-devexe/internal/backend"
	"github.com/fredrik/local-devexe/internal/config"
	"github.com/fredrik/local-devexe/internal/store"
	"github.com/fredrik/local-devexe/internal/vm/vmspec"
	"github.com/fredrik/local-devexe/internal/vsockproto"
)

// Preparer turns an image reference into boot artifacts. Implemented by
// the OCI pipeline; stubbed in tests.
type Preparer interface {
	// EnsureImage resolves ref, builds (or reuses) the shared read-only
	// base disk, and returns image info + the base disk path.
	EnsureImage(ctx context.Context, ref string) (vmspec.ImageInfo, string, error)
	// EnsureDataDisk creates the writable per-VM disk if missing.
	EnsureDataDisk(path string, sizeGB int) error
}

// GuestKeys supplies the authorized_keys lines delivered to every guest.
type GuestKeys func() []string

type Manager struct {
	cfg       *config.Config
	st        *store.Store
	be        backend.Backend
	prep      Preparer
	kernel    string
	guestKeys GuestKeys

	mu  sync.Mutex
	vms map[string]*entry
}

type entry struct {
	rec  *vmspec.VM
	run  backend.RunningVM
	busy string // non-empty: operation in progress
}

func NewManager(cfg *config.Config, st *store.Store, be backend.Backend, prep Preparer, kernelPath string, guestKeys GuestKeys) *Manager {
	return &Manager{
		cfg: cfg, st: st, be: be, prep: prep, kernel: kernelPath,
		guestKeys: guestKeys,
		vms:       map[string]*entry{},
	}
}

// Recover loads persisted VMs. Any VM recorded as running died with the
// previous daemon; the record is demoted to stopped.
func (m *Manager) Recover() error {
	recs, err := m.st.LoadVMs()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range recs {
		if rec.State != vmspec.StateStopped && rec.State != vmspec.StateError {
			rec.State = vmspec.StateStopped
			rec.LastStopReason = "daemon restart"
			rec.IP = ""
			if err := m.st.SaveVM(rec); err != nil {
				return err
			}
		}
		m.vms[rec.Spec.Name] = &entry{rec: rec}
	}
	return nil
}

// AutostartAll starts every VM marked autostart; failures are logged, not
// fatal.
func (m *Manager) AutostartAll(ctx context.Context) {
	for _, rec := range m.List() {
		if rec.Spec.Autostart && rec.State == vmspec.StateStopped {
			if err := m.Start(ctx, rec.Spec.Name); err != nil {
				log.Printf("autostart %s: %v", rec.Spec.Name, err)
			}
		}
	}
}

func (m *Manager) List() []vmspec.VM {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]vmspec.VM, 0, len(m.vms))
	for _, e := range m.vms {
		out = append(out, *e.rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Created.Before(out[j].Spec.Created) })
	return out
}

func (m *Manager) Get(name string) (vmspec.VM, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.vms[name]
	if !ok {
		return vmspec.VM{}, false
	}
	return *e.rec, true
}

// PoolUsage returns (usedCPUs, usedMemMB, usedDiskGB) across the fleet:
// CPU/RAM count while running, disk always.
func (m *Manager) PoolUsage() (int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.poolUsageLocked()
}

func (m *Manager) poolUsageLocked() (cpus, memMB, diskGB int) {
	for _, e := range m.vms {
		diskGB += e.rec.Spec.DiskGB
		switch e.rec.State {
		case vmspec.StateRunning, vmspec.StateStarting, vmspec.StateStopping:
			cpus += e.rec.Spec.CPUs
			memMB += e.rec.Spec.MemoryMB
		}
	}
	return
}

func (m *Manager) Pool() config.Pool { return m.cfg.Pool }

type CreateOpts struct {
	Name      string
	Image     string
	CPUs      int
	MemoryMB  int
	DiskGB    int
	Autostart bool
	NoStart   bool
	// Progress, when set, receives human-readable notes about slow steps
	// (image pulls, the one-time exeuntu bake).
	Progress io.Writer
}

// Create makes a new VM: resolves the image, builds disks, persists the
// record, and (by default) starts it.
func (m *Manager) Create(ctx context.Context, opts CreateOpts) (vmspec.VM, error) {
	spec := vmspec.Spec{
		Name:      opts.Name,
		Image:     opts.Image,
		CPUs:      opts.CPUs,
		MemoryMB:  opts.MemoryMB,
		DiskGB:    opts.DiskGB,
		Created:   time.Now().UTC(),
		Autostart: opts.Autostart,
	}
	if spec.Image == "" {
		spec.Image = m.cfg.DefaultImage
	}
	if spec.CPUs == 0 {
		spec.CPUs = m.cfg.DefaultCPUs
	}
	if spec.MemoryMB == 0 {
		spec.MemoryMB = m.cfg.DefaultMemoryMB
	}
	if spec.DiskGB == 0 {
		spec.DiskGB = m.cfg.DefaultDiskGB
	}
	if err := vmspec.ValidName(spec.Name); err != nil {
		return vmspec.VM{}, err
	}
	if err := m.be.Validate(spec); err != nil {
		return vmspec.VM{}, err
	}

	// Reserve the name and the disk quota.
	m.mu.Lock()
	if _, exists := m.vms[spec.Name]; exists {
		m.mu.Unlock()
		return vmspec.VM{}, fmt.Errorf("vm %q already exists", spec.Name)
	}
	if _, _, usedDisk := m.poolUsageLocked(); usedDisk+spec.DiskGB > m.cfg.Pool.DiskGB {
		m.mu.Unlock()
		return vmspec.VM{}, fmt.Errorf("pool exhausted: need %d GB disk, %d GB free (rm a vm or raise pool.disk_gb)",
			spec.DiskGB, m.cfg.Pool.DiskGB-usedDisk)
	}
	rec := &vmspec.VM{Spec: spec, State: vmspec.StateCreating}
	e := &entry{rec: rec, busy: "creating"}
	m.vms[spec.Name] = e
	m.mu.Unlock()

	fail := func(err error) (vmspec.VM, error) {
		m.mu.Lock()
		delete(m.vms, spec.Name)
		m.mu.Unlock()
		m.st.DeleteVM(spec.Name)
		return vmspec.VM{}, err
	}

	progress := opts.Progress
	if progress == nil {
		progress = io.Discard
	}
	info, _, err := m.ensureImage(ctx, spec.Image, progress)
	if err != nil {
		return fail(fmt.Errorf("image %s: %w", spec.Image, err))
	}
	rec.Image = info
	if err := m.prep.EnsureDataDisk(m.dataDiskPath(spec.Name), spec.DiskGB); err != nil {
		return fail(fmt.Errorf("data disk: %w", err))
	}

	m.mu.Lock()
	rec.State = vmspec.StateStopped
	e.busy = ""
	err = m.st.SaveVM(rec)
	m.mu.Unlock()
	if err != nil {
		return fail(err)
	}

	if !opts.NoStart {
		if err := m.Start(ctx, spec.Name); err != nil {
			return *rec, fmt.Errorf("created, but start failed: %w", err)
		}
	}
	out, _ := m.Get(spec.Name)
	return out, nil
}

// Start boots a stopped VM and waits for the guest to report ready.
func (m *Manager) Start(ctx context.Context, name string) error {
	m.mu.Lock()
	e, ok := m.vms[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no such vm %q", name)
	}
	if e.busy != "" {
		m.mu.Unlock()
		return fmt.Errorf("vm %q is busy (%s)", name, e.busy)
	}
	switch e.rec.State {
	case vmspec.StateRunning:
		m.mu.Unlock()
		return nil
	case vmspec.StateStopped, vmspec.StateError:
	default:
		m.mu.Unlock()
		return fmt.Errorf("vm %q is %s", name, e.rec.State)
	}
	usedCPU, usedMem, _ := m.poolUsageLocked()
	if usedCPU+e.rec.Spec.CPUs > m.cfg.Pool.CPUs {
		m.mu.Unlock()
		return fmt.Errorf("pool exhausted: need %d cpus, %d free (stop a vm or raise pool.cpus)",
			e.rec.Spec.CPUs, m.cfg.Pool.CPUs-usedCPU)
	}
	if usedMem+e.rec.Spec.MemoryMB > m.cfg.Pool.MemoryMB {
		m.mu.Unlock()
		return fmt.Errorf("pool exhausted: need %d MB memory, %d MB free (stop a vm or raise pool.memory_mb)",
			e.rec.Spec.MemoryMB, m.cfg.Pool.MemoryMB-usedMem)
	}
	e.busy = "starting"
	e.rec.State = vmspec.StateStarting
	rec := *e.rec
	m.mu.Unlock()

	// Rebuild the base disk path from the image digest (cache may have
	// been pruned; ensureImage is a fast no-op on cache hit).
	_, baseDisk, err := m.ensureImage(ctx, rec.Spec.Image, io.Discard)
	if err != nil {
		m.startFailed(e, fmt.Errorf("image: %w", err))
		return fmt.Errorf("image %s: %w", rec.Spec.Image, err)
	}

	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	run, err := m.be.Start(startCtx, backend.StartRequest{
		Spec:          rec.Spec,
		BaseDiskPath:  baseDisk,
		DataDiskPath:  m.dataDiskPath(name),
		KernelPath:    m.kernel,
		SerialLogPath: filepath.Join(m.st.VMDir(name), "serial.log"),
		GuestConfig: vsockproto.Config{
			Hostname:       name,
			AuthorizedKeys: m.guestKeys(),
			Entrypoint:     rec.Image.Entrypoint,
			Cmd:            rec.Image.Cmd,
			Env:            rec.Image.Env,
			WorkingDir:     rec.Image.WorkingDir,
			User:           m.cfg.DefaultUser,
		},
	})
	if err != nil {
		m.startFailed(e, err)
		return fmt.Errorf("start %s: %w (serial log: %s)", name, err, filepath.Join(m.st.VMDir(name), "serial.log"))
	}

	m.mu.Lock()
	e.run = run
	e.rec.State = vmspec.StateRunning
	e.rec.LastStopReason = ""
	if ip, ok := run.GuestIP(); ok {
		e.rec.IP = ip.String()
	}
	e.busy = ""
	m.st.SaveVM(e.rec)
	m.mu.Unlock()

	go m.watch(e, run)
	return nil
}

func (m *Manager) startFailed(e *entry, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.rec.State = vmspec.StateError
	e.rec.LastStopReason = cause.Error()
	e.busy = ""
	m.st.SaveVM(e.rec)
}

// watch waits for the VM to stop, however that happens, and settles the
// record.
func (m *Manager) watch(e *entry, run backend.RunningVM) {
	<-run.Done()
	m.settle(e, run)
}

// settle records that a VM stopped. Idempotent: called by the watcher and
// by Stop, whichever gets there first.
func (m *Manager) settle(e *entry, run backend.RunningVM) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.run != run {
		return // already settled, or superseded by a newer start
	}
	e.run = nil
	e.rec.IP = ""
	if e.rec.State == vmspec.StateRunning {
		e.rec.State = vmspec.StateStopped
		e.rec.LastStopReason = "guest powered off"
	} else if e.rec.State == vmspec.StateStopping {
		e.rec.State = vmspec.StateStopped
		e.rec.LastStopReason = "requested"
	}
	m.st.SaveVM(e.rec)
}

// Stop gracefully shuts a running VM down.
func (m *Manager) Stop(ctx context.Context, name string) error {
	m.mu.Lock()
	e, ok := m.vms[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no such vm %q", name)
	}
	if e.busy != "" {
		m.mu.Unlock()
		return fmt.Errorf("vm %q is busy (%s)", name, e.busy)
	}
	if e.rec.State != vmspec.StateRunning || e.run == nil {
		m.mu.Unlock()
		return fmt.Errorf("vm %q is not running", name)
	}
	run := e.run
	e.rec.State = vmspec.StateStopping
	e.busy = "stopping"
	m.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := run.Shutdown(stopCtx)

	m.mu.Lock()
	e.busy = ""
	m.mu.Unlock()
	if err != nil {
		return err
	}
	<-run.Done()
	m.settle(e, run)
	return nil
}

// Restart stops (if running) and starts a VM.
func (m *Manager) Restart(ctx context.Context, name string) error {
	if rec, ok := m.Get(name); ok && rec.State == vmspec.StateRunning {
		if err := m.Stop(ctx, name); err != nil {
			return err
		}
	}
	return m.Start(ctx, name)
}

// Remove deletes a VM and its disk. Running VMs are stopped first.
func (m *Manager) Remove(ctx context.Context, name string) error {
	m.mu.Lock()
	e, ok := m.vms[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no such vm %q", name)
	}
	if e.busy != "" {
		m.mu.Unlock()
		return fmt.Errorf("vm %q is busy (%s)", name, e.busy)
	}
	running := e.rec.State == vmspec.StateRunning
	m.mu.Unlock()

	if running {
		if err := m.Stop(ctx, name); err != nil {
			return err
		}
	}

	m.mu.Lock()
	delete(m.vms, name)
	m.mu.Unlock()
	return m.st.DeleteVM(name)
}

// EnsureRunning starts the VM if needed (used by the ssh broker and the
// HTTP front door) and returns its runtime handle.
func (m *Manager) EnsureRunning(ctx context.Context, name string) (backend.RunningVM, error) {
	m.mu.Lock()
	e, ok := m.vms[name]
	if ok && e.rec.State == vmspec.StateRunning && e.run != nil {
		run := e.run
		m.mu.Unlock()
		return run, nil
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no such vm %q", name)
	}
	if err := m.Start(ctx, name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.run == nil {
		return nil, fmt.Errorf("vm %q failed to stay running", name)
	}
	return e.run, nil
}

// Running returns the runtime handle of a running VM without starting it.
func (m *Manager) Running(name string) (backend.RunningVM, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.vms[name]
	if !ok || e.run == nil || e.rec.State != vmspec.StateRunning {
		return nil, false
	}
	return e.run, true
}

// UpdateShare mutates a VM's share config and persists it.
func (m *Manager) UpdateShare(name string, mutate func(*vmspec.Share)) (vmspec.VM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.vms[name]
	if !ok {
		return vmspec.VM{}, fmt.Errorf("no such vm %q", name)
	}
	mutate(&e.rec.Share)
	if err := m.st.SaveVM(e.rec); err != nil {
		return vmspec.VM{}, err
	}
	return *e.rec, nil
}

// StopAll shuts down every running VM (daemon shutdown path).
func (m *Manager) StopAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, rec := range m.List() {
		if rec.State != vmspec.StateRunning {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := m.Stop(ctx, name); err != nil {
				log.Printf("stop %s: %v", name, err)
			}
		}(rec.Spec.Name)
	}
	wg.Wait()
}

func (m *Manager) dataDiskPath(name string) string {
	return filepath.Join(m.st.VMDir(name), "data.img")
}
