//go:build linux

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
	gliderssh "github.com/gliderlabs/ssh"
	"github.com/mdlayher/vsock"
	gossh "golang.org/x/crypto/ssh"
)

// startSSHD serves ssh on :22 with an in-process Go server, so any image —
// alpine, distroless — is ssh-able without shipping an sshd. Auth checks
// the authorized keys the host delivered over vsock.
func startSSHD(authorizedKeys []string) error {
	var allowed []gliderssh.PublicKey
	for _, line := range authorizedKeys {
		key, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			fmt.Printf("exeguest: skipping bad authorized key: %v\n", err)
			continue
		}
		allowed = append(allowed, key)
	}
	if len(allowed) == 0 {
		return fmt.Errorf("no usable authorized keys delivered")
	}

	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	signer, err := gossh.NewSignerFromKey(hostKey)
	if err != nil {
		return err
	}

	server := &gliderssh.Server{
		Addr:    ":22",
		Handler: handleSession,
		PublicKeyHandler: func(ctx gliderssh.Context, key gliderssh.PublicKey) bool {
			for _, k := range allowed {
				if gliderssh.KeysEqual(k, key) {
					return true
				}
			}
			return false
		},
		SubsystemHandlers: map[string]gliderssh.SubsystemHandler{
			"sftp": handleSFTP,
		},
	}
	server.AddHostKey(signer)

	// Serve the same sshd on TCP :22 and on vsock port 22. The vsock
	// listener is the transport of last resort: macOS Local Network
	// privacy (TCC) can silently block host→guest TCP on the NAT bridge,
	// and vsock is exempt.
	tcpLn, err := net.Listen("tcp", ":22")
	if err != nil {
		return fmt.Errorf("listen :22: %w", err)
	}
	go func() {
		if err := server.Serve(tcpLn); err != nil {
			fmt.Printf("exeguest: sshd (tcp) exited: %v\n", err)
		}
	}()

	vsockLn, err := vsock.Listen(sshVsockPort, nil)
	if err != nil {
		fmt.Printf("exeguest: vsock ssh listener unavailable: %v\n", err)
	} else {
		go func() {
			if err := server.Serve(vsockLn); err != nil {
				fmt.Printf("exeguest: sshd (vsock) exited: %v\n", err)
			}
		}()
	}
	fmt.Println("exeguest: sshd listening on tcp :22 and vsock :22")
	return nil
}

const sshVsockPort = 22

// loginShell returns root's shell and home from /etc/passwd, with
// fallbacks that hold for any image.
func loginShell() (shell, home string) {
	shell, home = "/bin/sh", "/root"
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[0] == "root" {
			if fields[5] != "" {
				home = fields[5]
			}
			if fields[6] != "" {
				if _, err := os.Stat(fields[6]); err == nil {
					shell = fields[6]
				}
			}
			return
		}
	}
	return
}

func handleSession(s gliderssh.Session) {
	shell, home := loginShell()
	os.MkdirAll(home, 0o700)

	var cmd *exec.Cmd
	if len(s.Command()) > 0 {
		cmd = exec.Command(shell, "-c", s.RawCommand())
	} else {
		// Interactive: run the user's shell as a login shell (argv[0]
		// starts with "-") so profiles load.
		cmd = exec.Command(shell)
		cmd.Args = []string{"-" + filepath.Base(shell)}
	}
	cmd.Dir = home
	cmd.Env = append(cmd.Env,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME="+home,
		"USER=root",
		"LOGNAME=root",
		"SHELL="+shell,
		"LANG=C.UTF-8",
	)

	ptyReq, winCh, isPty := s.Pty()
	if isPty && len(s.Command()) == 0 {
		if motd, err := os.ReadFile("/etc/motd"); err == nil {
			s.Write(motd)
		}
	}
	if isPty {
		cmd.Env = append(cmd.Env, "TERM="+ptyReq.Term)
		f, err := pty.Start(cmd)
		if err != nil {
			fmt.Fprintf(s.Stderr(), "pty: %v\n", err)
			s.Exit(1)
			return
		}
		go func() {
			for win := range winCh {
				pty.Setsize(f, &pty.Winsize{Rows: uint16(win.Height), Cols: uint16(win.Width)})
			}
		}()
		go io.Copy(f, s)
		io.Copy(s, f)
	} else {
		cmd.Stdout = s
		cmd.Stderr = s.Stderr()
		stdin, err := cmd.StdinPipe()
		if err == nil {
			go func() {
				io.Copy(stdin, s)
				stdin.Close()
			}()
		}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(s.Stderr(), "exec: %v\n", err)
			s.Exit(127)
			return
		}
	}
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			s.Exit(exitErr.ExitCode())
			return
		}
		s.Exit(1)
		return
	}
	s.Exit(0)
}
