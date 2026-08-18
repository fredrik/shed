// exeguest is the devexe guest agent. It is built GOOS=linux GOARCH=arm64
// CGO_ENABLED=0 and packed into the initramfs as /init, where it runs as
// pid 1: it assembles the root filesystem from the base (ro) and data (rw)
// disks, brings up networking, serves ssh, and supervises the image
// workload.

//go:build linux

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func main() {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "exeguest: must run as pid 1 inside a VM")
		os.Exit(1)
	}
	// Wire stdio to the console before anything else; without this,
	// nothing the agent prints is visible on the host. The initramfs
	// carries a /dev/console node so this works before devtmpfs.
	openConsole()
	if err := boot(); err != nil {
		fmt.Fprintf(os.Stderr, "exeguest: boot failed: %v\n", err)
	}
	powerOff()
}

func openConsole() {
	fd, err := unix.Open("/dev/console", unix.O_RDWR, 0)
	if err != nil {
		return
	}
	for _, target := range []int{0, 1, 2} {
		unix.Dup2(fd, target)
	}
	if fd > 2 {
		unix.Close(fd)
	}
}

func boot() error {
	fmt.Println("exeguest: starting")
	mounts := []struct{ src, dst, fstype string }{
		{"proc", "/proc", "proc"},
		{"sysfs", "/sys", "sysfs"},
		{"devtmpfs", "/dev", "devtmpfs"},
	}
	for _, m := range mounts {
		if err := unix.Mount(m.src, m.dst, m.fstype, 0, ""); err != nil {
			return fmt.Errorf("mount %s: %w", m.dst, err)
		}
	}

	if _, err := os.Stat("/dev/vda"); err == nil {
		return bootFromDisks()
	}
	return helloMode()
}

// helloMode is the diskless smoke test: prove the kernel+initramfs boot
// path, then power off.
func helloMode() error {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return fmt.Errorf("uname: %w", err)
	}
	fmt.Printf("exeguest: hello mode, kernel %s %s\n", cstr(uts.Release[:]), cstr(uts.Machine[:]))
	fmt.Println("exeguest: OK, powering off")
	return nil
}

func powerOff() {
	unix.Sync()
	unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	// Reboot only returns on failure; block so pid 1 never exits (the
	// kernel panics if it does).
	select {}
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
