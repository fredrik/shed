//go:build linux

package main

import (
	"fmt"
	"io"

	gliderssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
)

// handleSFTP serves the sftp subsystem in-process, which also covers
// modern scp (OpenSSH ≥ 9 rides scp over sftp).
func handleSFTP(s gliderssh.Session) {
	srv, err := sftp.NewServer(s)
	if err != nil {
		fmt.Printf("exeguest: sftp new: %v\n", err)
		fmt.Fprintf(s.Stderr(), "sftp: %v\n", err)
		return
	}
	defer srv.Close()
	if err := srv.Serve(); err != nil && err != io.EOF {
		fmt.Printf("exeguest: sftp serve: %v\n", err)
	}
}
