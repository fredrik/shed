// Package vzbackend boots devexe microVMs with Apple's Virtualization
// framework via Code-Hex/vz. Binaries importing it must be signed with the
// com.apple.security.virtualization entitlement (make handles this).
package vzbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Code-Hex/vz/v3"

	"github.com/fredrik/local-devexe/internal/backend"
	"github.com/fredrik/local-devexe/internal/vm/vmspec"
	"github.com/fredrik/local-devexe/internal/vsockproto"
)

type Backend struct {
	KernelPath    string
	InitramfsPath string
}

func New(kernelPath, initramfsPath string) *Backend {
	return &Backend{KernelPath: kernelPath, InitramfsPath: initramfsPath}
}

func (b *Backend) Name() string { return "vz" }

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

func (b *Backend) Start(ctx context.Context, req backend.StartRequest) (backend.RunningVM, error) {
	bootLoader, err := vz.NewLinuxBootLoader(b.KernelPath,
		vz.WithCommandLine("console=hvc0 rdinit=/init"),
		vz.WithInitrd(b.InitramfsPath),
	)
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	config, err := vz.NewVirtualMachineConfiguration(bootLoader,
		uint(req.Spec.CPUs), uint64(req.Spec.MemoryMB)*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("vm config: %w", err)
	}

	serial, err := vz.NewFileSerialPortAttachment(req.SerialLogPath, false)
	if err != nil {
		return nil, fmt.Errorf("serial log: %w", err)
	}
	console, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serial)
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{console})

	var disks []vz.StorageDeviceConfiguration
	for _, d := range []struct {
		path string
		ro   bool
	}{{req.BaseDiskPath, true}, {req.DataDiskPath, false}} {
		att, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(
			d.path, d.ro, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync)
		if err != nil {
			return nil, fmt.Errorf("disk %s: %w", d.path, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return nil, fmt.Errorf("blk %s: %w", d.path, err)
		}
		disks = append(disks, blk)
	}
	config.SetStorageDevicesVirtualMachineConfiguration(disks)

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("nat: %w", err)
	}
	netdev, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("netdev: %w", err)
	}
	mac, err := vz.NewMACAddress(req.Spec.MAC())
	if err != nil {
		return nil, fmt.Errorf("mac: %w", err)
	}
	netdev.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netdev})

	vsockDev, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("vsock: %w", err)
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockDev})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("entropy: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	if _, err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return nil, fmt.Errorf("new vm: %w", err)
	}
	if err := vm.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	r := &runningVM{
		vm:   vm,
		done: make(chan struct{}),
	}
	go r.watch()

	if err := r.handshake(ctx, req.GuestConfig); err != nil {
		vm.Stop()
		return nil, fmt.Errorf("guest handshake: %w", err)
	}
	return r, nil
}

type runningVM struct {
	vm      *vz.VirtualMachine
	done    chan struct{}
	ip      net.IP
	control net.Conn
	enc     *json.Encoder
}

func (r *runningVM) watch() {
	for state := range r.vm.StateChangedNotify() {
		if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
			close(r.done)
			return
		}
	}
	close(r.done)
}

// handshake accepts the guest's vsock control connection, delivers its
// config, and waits for the ready report.
func (r *runningVM) handshake(ctx context.Context, guestCfg vsockproto.Config) error {
	devs := r.vm.SocketDevices()
	if len(devs) == 0 {
		return errors.New("no vsock device")
	}
	ln, err := devs[0].Listen(vsockproto.Port)
	if err != nil {
		return fmt.Errorf("vsock listen: %w", err)
	}

	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		connCh <- c
	}()

	var conn net.Conn
	select {
	case conn = <-connCh:
	case err := <-errCh:
		return fmt.Errorf("vsock accept: %w", err)
	case <-r.done:
		return errors.New("vm stopped during boot (see serial log)")
	case <-ctx.Done():
		return fmt.Errorf("boot: %w", ctx.Err())
	}

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var hello vsockproto.Message
	if err := dec.Decode(&hello); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if hello.Type != vsockproto.TypeHello {
		return fmt.Errorf("expected hello, got %q", hello.Type)
	}
	if err := enc.Encode(vsockproto.Message{Type: vsockproto.TypeConfig, Config: &guestCfg}); err != nil {
		return fmt.Errorf("send config: %w", err)
	}

	readyCh := make(chan vsockproto.Message, 1)
	readyErr := make(chan error, 1)
	go func() {
		var msg vsockproto.Message
		if err := dec.Decode(&msg); err != nil {
			readyErr <- err
			return
		}
		readyCh <- msg
	}()
	select {
	case msg := <-readyCh:
		switch msg.Type {
		case vsockproto.TypeReady:
			r.ip = net.ParseIP(msg.IP)
			r.control = conn
			r.enc = enc
			return nil
		case vsockproto.TypeError:
			return fmt.Errorf("guest: %s", msg.Error)
		default:
			return fmt.Errorf("unexpected message %q", msg.Type)
		}
	case err := <-readyErr:
		return fmt.Errorf("await ready: %w", err)
	case <-r.done:
		return errors.New("vm stopped during boot (see serial log)")
	case <-ctx.Done():
		return fmt.Errorf("boot: %w", ctx.Err())
	}
}

func (r *runningVM) Shutdown(ctx context.Context) error {
	if r.enc != nil {
		r.enc.Encode(vsockproto.Message{Type: vsockproto.TypeShutdown})
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return r.Kill()
	}
}

func (r *runningVM) Kill() error {
	select {
	case <-r.done:
		return nil
	default:
	}
	if err := r.vm.Stop(); err != nil {
		return fmt.Errorf("force stop: %w", err)
	}
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}

func (r *runningVM) Done() <-chan struct{} { return r.done }

func (r *runningVM) GuestIP() (net.IP, bool) { return r.ip, r.ip != nil }

// DialGuest reaches a TCP port in the guest: direct NAT TCP when macOS
// allows it, vsock forward through the agent otherwise.
func (r *runningVM) DialGuest(ctx context.Context, port int) (net.Conn, error) {
	if r.ip != nil {
		d := net.Dialer{Timeout: 750 * time.Millisecond}
		if conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(r.ip.String(), fmt.Sprint(port))); err == nil {
			return conn, nil
		}
	}
	devs := r.vm.SocketDevices()
	if len(devs) == 0 {
		return nil, errors.New("no vsock device")
	}
	conn, err := devs[0].Connect(vsockproto.ForwardPort)
	if err != nil {
		return nil, fmt.Errorf("vsock forward connect: %w", err)
	}
	if err := json.NewEncoder(conn).Encode(vsockproto.ForwardHeader{Port: port}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send forward header: %w", err)
	}
	return conn, nil
}
