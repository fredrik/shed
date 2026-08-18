//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	gliderssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
)

// agentPath is where stage 1 parks the agent binary in the real root, so
// it can be re-executed after switch_root.
const agentPath = "/.shed/agent"

// handleSFTP serves the sftp subsystem, which also covers modern scp
// (OpenSSH ≥ 9 rides scp over sftp). Root sessions are served in-process;
// non-root sessions re-exec the agent with dropped privileges so file
// ownership and permissions are the user's.
func handleSFTP(s gliderssh.Session) {
	u := sessionTarget
	if u.UID == 0 {
		srv, err := sftp.NewServer(s)
		if err != nil {
			fmt.Fprintf(s.Stderr(), "sftp: %v\n", err)
			return
		}
		defer srv.Close()
		if err := srv.Serve(); err != nil && err != io.EOF {
			fmt.Printf("shedguest: sftp serve: %v\n", err)
		}
		return
	}

	cmd := exec.Command(agentPath, "sftp-server")
	cmd.Dir = u.Home
	cmd.Env = []string{"HOME=" + u.Home, "USER=" + u.Name, "PATH=/usr/bin:/bin"}
	applyCredential(cmd, u)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	cmd.Stdout = s
	cmd.Stderr = s.Stderr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(s.Stderr(), "sftp: %v\n", err)
		s.Exit(1)
		return
	}
	go func() {
		io.Copy(stdin, s)
		stdin.Close()
	}()
	if err := cmd.Wait(); err != nil {
		s.Exit(1)
		return
	}
	s.Exit(0)
}

// stdioConn adapts the process's stdin/stdout into the ReadWriteCloser
// the sftp server wants (used in sftp-server re-exec mode).
type stdioConn struct{}

func (stdioConn) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdioConn) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdioConn) Close() error                { return os.Stdout.Close() }

// runSFTPServer is the re-exec entry point: serve sftp over stdio, already
// running as the target user.
func runSFTPServer() {
	srv, err := sftp.NewServer(stdioConn{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-server: %v\n", err)
		os.Exit(1)
	}
	if err := srv.Serve(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "sftp-server: %v\n", err)
		os.Exit(1)
	}
}
