package initramfs

import (
	"fmt"
	"io"
)

// cpioWriter writes a newc-format (SVR4, "070701") cpio archive, the format
// the Linux kernel unpacks as an initramfs. Hand-rolled so we control every
// header field (device nodes, directories) without a dependency.
type cpioWriter struct {
	w   io.Writer
	ino uint32
}

const newcMagic = "070701"

func (cw *cpioWriter) entry(name string, mode uint32, rdevMajor, rdevMinor uint32, data []byte) error {
	cw.ino++
	hdr := fmt.Sprintf("%s%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		newcMagic,
		cw.ino,      // ino
		mode,        // mode
		0,           // uid
		0,           // gid
		1,           // nlink
		0,           // mtime
		len(data),   // filesize
		0,           // devmajor
		0,           // devminor
		rdevMajor,   // rdevmajor
		rdevMinor,   // rdevminor
		len(name)+1, // namesize (incl. NUL)
		0,           // check (unused in newc)
	)
	if _, err := io.WriteString(cw.w, hdr); err != nil {
		return err
	}
	if _, err := io.WriteString(cw.w, name+"\x00"); err != nil {
		return err
	}
	// Header (110 bytes) + name is padded to a multiple of 4.
	if err := cw.pad(110 + len(name) + 1); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, err := cw.w.Write(data); err != nil {
			return err
		}
		if err := cw.pad(len(data)); err != nil {
			return err
		}
	}
	return nil
}

func (cw *cpioWriter) pad(n int) error {
	if rem := n % 4; rem != 0 {
		_, err := cw.w.Write(make([]byte, 4-rem))
		return err
	}
	return nil
}

func (cw *cpioWriter) File(name string, perm uint32, data []byte) error {
	const sIFREG = 0o100000
	return cw.entry(name, sIFREG|perm, 0, 0, data)
}

func (cw *cpioWriter) Dir(name string, perm uint32) error {
	const sIFDIR = 0o040000
	return cw.entry(name, sIFDIR|perm, 0, 0, nil)
}

func (cw *cpioWriter) CharDev(name string, perm uint32, major, minor uint32) error {
	const sIFCHR = 0o020000
	return cw.entry(name, sIFCHR|perm, major, minor, nil)
}

func (cw *cpioWriter) Trailer() error {
	return cw.entry("TRAILER!!!", 0, 0, 0, nil)
}
