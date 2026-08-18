// spike is the throwaway M0 de-risk program: it proves OCI image → microVM
// → ssh works end to end on this machine before the real daemon is built.
//
//	spike                  # stage 1: kernel + initramfs hello, power off
//	spike -image alpine:latest        # stage 2: overlay root from image
//	spike -image alpine:latest        # run again: proves disk persistence
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/fredrik/shed/internal/diskfs"
	"github.com/fredrik/shed/internal/image"
	"github.com/fredrik/shed/internal/initramfs"
)

func main() {
	home, _ := os.UserHomeDir()
	kernel := flag.String("kernel", filepath.Join(home, "Library/Caches/shed/kernel/3.28.0/Image"), "uncompressed arm64 kernel Image")
	cmdline := flag.String("cmdline", "console=hvc0 rdinit=/init", "kernel command line")
	imageRef := flag.String("image", "", "OCI image to boot (empty = diskless hello mode)")
	workdir := flag.String("workdir", filepath.Join(home, "Library/Caches/shed/spike"), "scratch dir for disks")
	reset := flag.Bool("reset", false, "delete the data disk first (fresh first boot)")
	timeout := flag.Duration("timeout", 120*time.Second, "boot timeout")
	flag.Parse()

	if err := run(*kernel, *cmdline, *imageRef, *workdir, *reset, *timeout); err != nil {
		log.Fatalf("spike: %v", err)
	}
}

func run(kernel, cmdline, imageRef, workdir string, reset bool, timeout time.Duration) error {
	initrd := filepath.Join(os.TempDir(), "shed-spike-initramfs.cpio")
	if err := initramfs.WriteTo(initrd); err != nil {
		return fmt.Errorf("build initramfs: %w", err)
	}

	var disks []vz.StorageDeviceConfiguration
	if imageRef != "" {
		baseDisk, dataDisk, err := prepareDisks(imageRef, workdir, reset)
		if err != nil {
			return err
		}
		for _, d := range []struct {
			path string
			ro   bool
		}{{baseDisk, true}, {dataDisk, false}} {
			att, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(
				d.path, d.ro, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync)
			if err != nil {
				return fmt.Errorf("disk attachment %s: %w", d.path, err)
			}
			blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
			if err != nil {
				return fmt.Errorf("blk config %s: %w", d.path, err)
			}
			disks = append(disks, blk)
		}
	}

	bootLoader, err := vz.NewLinuxBootLoader(kernel,
		vz.WithCommandLine(cmdline),
		vz.WithInitrd(initrd),
	)
	if err != nil {
		return fmt.Errorf("boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, 1, 1024*1024*1024)
	if err != nil {
		return fmt.Errorf("vm config: %w", err)
	}

	serial, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stdout)
	if err != nil {
		return fmt.Errorf("serial attachment: %w", err)
	}
	console, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serial)
	if err != nil {
		return fmt.Errorf("console config: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{console})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("entropy config: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	if len(disks) > 0 {
		config.SetStorageDevicesVirtualMachineConfiguration(disks)

		nat, err := vz.NewNATNetworkDeviceAttachment()
		if err != nil {
			return fmt.Errorf("nat attachment: %w", err)
		}
		netdev, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
		if err != nil {
			return fmt.Errorf("net config: %w", err)
		}
		mac, err := vz.NewRandomLocallyAdministeredMACAddress()
		if err != nil {
			return fmt.Errorf("mac: %w", err)
		}
		netdev.SetMACAddress(mac)
		config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netdev})

		vsockDev, err := vz.NewVirtioSocketDeviceConfiguration()
		if err != nil {
			return fmt.Errorf("vsock config: %w", err)
		}
		config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockDev})
	}

	if _, err := config.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fmt.Errorf("new vm: %w", err)
	}

	start := time.Now()
	if err := vm.Start(); err != nil {
		return fmt.Errorf("start vm: %w", err)
	}
	log.Printf("spike: VM started")

	// With disks attached, the guest brings up ssh and reports over vsock;
	// run the ssh round-trip test, then ask it to shut down.
	testErr := make(chan error, 1)
	if imageRef != "" {
		go func() { testErr <- sshRoundTrip(vm, start) }()
	} else {
		close(testErr)
	}

	deadline := time.After(timeout)
	for {
		select {
		case err := <-testErr:
			if err != nil {
				return fmt.Errorf("ssh round trip: %w", err)
			}
			testErr = nil // test done; keep waiting for power-off
		case state := <-vm.StateChangedNotify():
			if state == vz.VirtualMachineStateStopped {
				log.Printf("spike: guest powered off cleanly after %s", time.Since(start).Round(time.Millisecond))
				return nil
			}
			if state == vz.VirtualMachineStateError {
				return fmt.Errorf("vm entered error state")
			}
		case <-deadline:
			return fmt.Errorf("timed out after %s", timeout)
		}
	}
}

// prepareDisks pulls the image and builds the base (ro, cached by digest)
// and data (rw, persistent across spike runs) disks.
func prepareDisks(imageRef, workdir string, reset bool) (baseDisk, dataDisk string, err error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return "", "", err
	}

	t0 := time.Now()
	img, err := image.Pull(context.Background(), imageRef)
	if err != nil {
		return "", "", err
	}
	digest, err := img.Digest()
	if err != nil {
		return "", "", err
	}
	log.Printf("spike: resolved %s → %s (%s)", imageRef, digest[:19], time.Since(t0).Round(time.Millisecond))

	baseDisk = filepath.Join(workdir, "base-"+strings.TrimPrefix(digest, "sha256:")[:16]+".img")
	if _, statErr := os.Stat(baseDisk); statErr != nil {
		t1 := time.Now()
		tarStream := img.Flatten()
		defer tarStream.Close()
		if err := diskfs.BuildBaseDisk(tarStream, baseDisk, 16*1024*1024*1024); err != nil {
			return "", "", fmt.Errorf("build base disk: %w", err)
		}
		fi, _ := os.Stat(baseDisk)
		log.Printf("spike: built base disk %s (%d MB, %s)", filepath.Base(baseDisk), fi.Size()>>20, time.Since(t1).Round(time.Millisecond))
	} else {
		log.Printf("spike: base disk cache hit: %s", filepath.Base(baseDisk))
	}

	dataDisk = filepath.Join(workdir, "data.img")
	if reset {
		os.Remove(dataDisk)
	}
	if _, statErr := os.Stat(dataDisk); statErr != nil {
		t2 := time.Now()
		if err := diskfs.NewDataDisk(dataDisk, 1*1024*1024*1024); err != nil {
			return "", "", fmt.Errorf("build data disk: %w", err)
		}
		log.Printf("spike: created 1 GB data disk (%s)", time.Since(t2).Round(time.Millisecond))
	} else {
		log.Printf("spike: reusing existing data disk (persistence test)")
	}
	return baseDisk, dataDisk, nil
}
