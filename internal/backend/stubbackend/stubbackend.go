// Package stubbackend fakes VMs for tests: instant boots, controllable
// lifecycle, no virtualization.
package stubbackend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/fredrik/local-devexe/internal/backend"
	"github.com/fredrik/local-devexe/internal/vm/vmspec"
)

type Backend struct {
	mu      sync.Mutex
	started int
	// FailStart makes the next Start fail.
	FailStart error
	// Dial serves DialGuest connections; nil rejects them.
	Dial func(port int) (net.Conn, error)
}

func New() *Backend { return &Backend{} }

func (b *Backend) Name() string { return "stub" }

func (b *Backend) Validate(spec vmspec.Spec) error {
	if spec.CPUs < 1 {
		return errors.New("cpus must be >= 1")
	}
	if spec.MemoryMB < 128 {
		return errors.New("memory must be >= 128 MB")
	}
	if spec.DiskGB < 1 {
		return errors.New("disk must be >= 1 GB")
	}
	return nil
}

func (b *Backend) Started() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

func (b *Backend) Start(ctx context.Context, req backend.StartRequest) (backend.RunningVM, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.FailStart != nil {
		err := b.FailStart
		b.FailStart = nil
		return nil, err
	}
	b.started++
	return &VM{
		name: req.Spec.Name,
		ip:   net.IPv4(198, 51, 100, byte(b.started)),
		done: make(chan struct{}),
		dial: b.Dial,
	}, nil
}

type VM struct {
	name string
	ip   net.IP
	dial func(port int) (net.Conn, error)

	mu   sync.Mutex
	done chan struct{}
}

// StopFromGuest simulates the guest powering off on its own.
func (v *VM) StopFromGuest() { v.close() }

func (v *VM) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	select {
	case <-v.done:
	default:
		close(v.done)
	}
}

func (v *VM) Shutdown(ctx context.Context) error { v.close(); return nil }
func (v *VM) Kill() error                        { v.close(); return nil }
func (v *VM) Done() <-chan struct{}              { return v.done }
func (v *VM) GuestIP() (net.IP, bool)            { return v.ip, true }

func (v *VM) DialGuest(ctx context.Context, port int) (net.Conn, error) {
	if v.dial == nil {
		return nil, fmt.Errorf("stub vm %s has no dialer", v.name)
	}
	return v.dial(port)
}
