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
	"syscall"

	"github.com/creack/pty"
	gliderssh "github.com/gliderlabs/ssh"
	"github.com/mdlayher/vsock"
	gossh "golang.org/x/crypto/ssh"
)

// startSSHD serves ssh on :22 with an in-process Go server, so any image —
// alpine, distroless — is ssh-able without shipping an sshd. Auth checks
// the authorized keys the host delivered over vsock; sessions run as the
// host-requested login user when the image has it.
func startSSHD(authorizedKeys []string, preferredUser string) error {
	resolveSessionTarget(preferredUser)
	var allowed []gliderssh.PublicKey
	for _, line := range authorizedKeys {
		key, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			fmt.Printf("shedguest: skipping bad authorized key: %v\n", err)
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
			fmt.Printf("shedguest: sshd (tcp) exited: %v\n", err)
		}
	}()

	vsockLn, err := vsock.Listen(sshVsockPort, nil)
	if err != nil {
		fmt.Printf("shedguest: vsock ssh listener unavailable: %v\n", err)
	} else {
		go func() {
			if err := server.Serve(vsockLn); err != nil {
				fmt.Printf("shedguest: sshd (vsock) exited: %v\n", err)
			}
		}()
	}
	fmt.Println("shedguest: sshd listening on tcp :22 and vsock :22")
	return nil
}

const sshVsockPort = 22

// applyCredential makes cmd run as u when u isn't root. The agent stays
// root; only session children drop privileges.
func applyCredential(cmd *exec.Cmd, u *guestUser) {
	if u.UID == 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    u.UID,
		Gid:    u.GID,
		Groups: u.Groups,
	}
}

func handleSession(s gliderssh.Session) {
	u := sessionTarget

	var cmd *exec.Cmd
	if len(s.Command()) > 0 {
		cmd = exec.Command(u.Shell, "-c", s.RawCommand())
	} else {
		// Interactive: run the user's shell as a login shell (argv[0]
		// starts with "-") so profiles load.
		cmd = exec.Command(u.Shell)
		cmd.Args = []string{"-" + filepath.Base(u.Shell)}
	}
	cmd.Dir = u.Home
	// User-local bins and mise shims lead the PATH so managed tools work
	// in non-interactive exec too, where .bashrc never runs.
	path := u.Home + "/.local/bin:" + u.Home + "/.local/share/mise/shims" +
		":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	cmd.Env = append(cmd.Env,
		"PATH="+path,
		"HOME="+u.Home,
		"USER="+u.Name,
		"LOGNAME="+u.Name,
		"SHELL="+u.Shell,
		"LANG=C.UTF-8",
	)
	applyCredential(cmd, u)

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
