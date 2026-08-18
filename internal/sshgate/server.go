// Package sshgate is the daemon's front door on 127.0.0.1:2222. It routes
// by SSH username, sshpiper-style: the reserved user "exe" reaches the
// control plane; any other username names a VM and the session is brokered
// into that VM's sshd.
package sshgate

import (
	"context"
	"fmt"
	"log"

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

	srv *gliderssh.Server
}

func (s *Server) ListenAndServe() error {
	s.srv = &gliderssh.Server{
		Addr:             s.Addr,
		Handler:          s.handle,
		PublicKeyHandler: s.auth,
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
	s.srv.AddHostKey(s.HostSigner)
	log.Printf("sshgate: listening on %s", s.Addr)
	return s.srv.ListenAndServe()
}

func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
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
