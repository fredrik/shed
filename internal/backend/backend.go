// Package backend defines the seam between the VM manager and the
// hypervisor. The vz backend is the real one; stubbackend serves tests and
// keeps the door open for qemu or container-based fallbacks.
package backend

import (
	"context"
	"net"

	"github.com/fredrik/shed/internal/vm/vmspec"
	"github.com/fredrik/shed/internal/vsockproto"
)

type Backend interface {
	Name() string
	// Validate checks the spec against backend capabilities before any
	// resources are committed.
	Validate(spec vmspec.Spec) error
	// Start boots the VM and blocks until the guest agent reports ready
	// (or ctx expires).
	Start(ctx context.Context, req StartRequest) (RunningVM, error)
}

type StartRequest struct {
	Spec          vmspec.Spec
	BaseDiskPath  string // read-only ext4 built from the OCI image (vda)
	DataDiskPath  string // writable per-VM ext4 (vdb)
	KernelPath    string
	SerialLogPath string
	GuestConfig   vsockproto.Config
}

type RunningVM interface {
	// Shutdown asks the guest agent to power off and waits; on timeout it
	// falls back to Kill.
	Shutdown(ctx context.Context) error
	Kill() error
	// Done is closed when the VM has fully stopped, however that happened.
	Done() <-chan struct{}
	GuestIP() (net.IP, bool)
	// DialGuest opens a connection to a TCP port inside the guest. The
	// transport (NAT TCP, vsock forward) is the backend's business.
	DialGuest(ctx context.Context, port int) (net.Conn, error)
}
