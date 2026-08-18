// Package sshgate is the daemon's front door. It routes by SSH username,
// sshpiper-style: the reserved user "shed" reaches the control plane; any
// other username names a VM and the session is brokered into that VM's
// sshd. The gateway serves two listeners with the same handler: TCP
// 127.0.0.1:2222 with public-key auth, and a unix socket with no client
// auth for the local shed client (the socket's file mode is the auth).
package sshgate

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	gliderssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"

	"github.com/fredrik/shed/internal/control"
	"github.com/fredrik/shed/internal/keys"
	"github.com/fredrik/shed/internal/vm"
)

// ControlUsers are the reserved usernames for the control plane.
var ControlUsers = map[string]bool{"shed": true}

type Server struct {
	Addr               string
	HostSigner         gossh.Signer
	BrokerSigner       gossh.Signer
	AuthorizedKeysPath string
	Mgr                *vm.Manager
	ControlDeps        control.Deps

	srv  *gliderssh.Server // TCP, public-key auth
	sock *gliderssh.Server // unix socket, no client auth
}

func (s *Server) ListenAndServe() error {
	s.srv = s.newServer(true)
	s.srv.Addr = s.Addr
	log.Printf("sshgate: listening on %s", s.Addr)
	return s.srv.ListenAndServe()
}

// ServeSocket serves the gateway on a unix socket with client auth
// disabled: being able to open the 0600 socket is the auth. gliderlabs
// sets NoClientAuth when a server has no auth handlers, which is why the
// socket needs its own server value rather than a second listener.
func (s *Server) ServeSocket(path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		os.Remove(path) // stale socket from a previous run
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return err
	}
	s.sock = s.newServer(false)
	log.Printf("sshgate: listening on %s", path)
	return s.sock.Serve(l)
}

func (s *Server) newServer(withAuth bool) *gliderssh.Server {
	srv := &gliderssh.Server{
		Handler: s.handle,
		SubsystemHandlers: map[string]gliderssh.SubsystemHandler{
			"sftp": func(sess gliderssh.Session) {
				if ControlUsers[sess.User()] {
					fmt.Fprintf(sess.Stderr(), "shed: sftp works on vm sessions (sftp <vm>@shed)\r\n")
					sess.Exit(1)
					return
				}
				s.brokerSubsystem(context.Background(), sess, sess.User(), "sftp")
			},
		},
		ChannelHandlers: map[string]gliderssh.ChannelHandler{
			"session":      gliderssh.DefaultSessionHandler,
			"direct-tcpip": s.handleDirectTCPIP,
		},
	}
	if withAuth {
		srv.PublicKeyHandler = s.auth
	}
	srv.AddHostKey(s.HostSigner)
	return srv
}

func (s *Server) Close() error {
	var firstErr error
	for _, srv := range []*gliderssh.Server{s.srv, s.sock} {
		if srv != nil {
			if err := srv.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *Server) auth(ctx gliderssh.Context, key gliderssh.PublicKey) bool {
	allowed, err := keys.LoadAuthorizedKeys(s.AuthorizedKeysPath)
	if err != nil {
		log.Printf("sshgate: read authorized_keys: %v", err)
		return false
	}
	for _, k := range allowed {
		if gliderssh.KeysEqual(k, key) {
			return true
		}
	}
	log.Printf("sshgate: rejected key %s for user %s", gossh.FingerprintSHA256(key), ctx.User())
	return false
}

func (s *Server) handle(sess gliderssh.Session) {
	user := sess.User()
	if ControlUsers[user] {
		code := control.Run(sess, s.ControlDeps)
		sess.Exit(code)
		return
	}
	s.brokerSession(context.Background(), sess, user)
}
