package sshgate

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// connectVM dials the named VM's sshd (starting the VM if needed) and
// returns an SSH client authenticated as root with the broker key.
func (s *Server) connectVM(ctx context.Context, vmName string) (*gossh.Client, error) {
	if _, ok := s.Mgr.Get(vmName); !ok {
		return nil, fmt.Errorf("no such vm %q (create it: ssh shed new %s)", vmName, vmName)
	}
	startCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	run, err := s.Mgr.EnsureRunning(startCtx, vmName)
	if err != nil {
		return nil, err
	}
	raw, err := run.DialGuest(startCtx, 22)
	if err != nil {
		return nil, fmt.Errorf("dial vm sshd: %w", err)
	}
	clientCfg := &gossh.ClientConfig{
		User:            "root",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(s.BrokerSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), // VM host keys are ephemeral; trust is the vsock/NAT channel
		Timeout:         15 * time.Second,
	}
	conn, chans, reqs, err := gossh.NewClientConn(raw, vmName+":22", clientCfg)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("ssh to vm: %w", err)
	}
	return gossh.NewClient(conn, chans, reqs), nil
}

// brokerSession proxies an incoming session into the named VM's sshd. The
// daemon is the SSH client, authenticating as root with the broker key that
// every VM trusts.
func (s *Server) brokerSession(ctx context.Context, sess gliderssh.Session, vmName string) {
	client, err := s.connectVM(ctx, vmName)
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "shed: %v\r\n", err)
		sess.Exit(1)
		return
	}
	defer client.Close()

	cs, err := client.NewSession()
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "shed: vm session: %v\r\n", err)
		sess.Exit(1)
		return
	}
	defer cs.Close()

	for _, kv := range sess.Environ() {
		if k, v, ok := splitEnv(kv); ok {
			cs.Setenv(k, v) // guest may reject; best effort
		}
	}

	ptyReq, winCh, isPty := sess.Pty()
	if isPty {
		modes := gossh.TerminalModes{}
		if err := cs.RequestPty(ptyReq.Term, ptyReq.Window.Height, ptyReq.Window.Width, modes); err != nil {
			fmt.Fprintf(sess.Stderr(), "shed: pty: %v\r\n", err)
			sess.Exit(1)
			return
		}
		go func() {
			for win := range winCh {
				cs.WindowChange(win.Height, win.Width)
			}
		}()
	}

	cs.Stdin = sess
	cs.Stdout = sess
	cs.Stderr = sess.Stderr()

	if cmd := sess.RawCommand(); cmd != "" {
		err = cs.Start(cmd)
	} else {
		err = cs.Shell()
	}
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "shed: start session: %v\r\n", err)
		sess.Exit(1)
		return
	}

	err = cs.Wait()
	switch e := err.(type) {
	case nil:
		sess.Exit(0)
	case *gossh.ExitError:
		sess.Exit(e.ExitStatus())
	case *gossh.ExitMissingError:
		sess.Exit(1)
	default:
		if err != io.EOF {
			log.Printf("sshgate: session to %s ended: %v", vmName, err)
		}
		sess.Exit(1)
	}
}

// brokerSubsystem forwards a subsystem request (sftp — which also carries
// modern scp) into the VM.
func (s *Server) brokerSubsystem(ctx context.Context, sess gliderssh.Session, vmName, subsystem string) {
	client, err := s.connectVM(ctx, vmName)
	if err != nil {
		log.Printf("sshgate: subsystem %s → %s: connect: %v", subsystem, vmName, err)
		fmt.Fprintf(sess.Stderr(), "shed: %v\r\n", err)
		sess.Exit(1)
		return
	}
	defer client.Close()

	cs, err := client.NewSession()
	if err != nil {
		log.Printf("sshgate: subsystem %s → %s: session: %v", subsystem, vmName, err)
		sess.Exit(1)
		return
	}
	defer cs.Close()

	// x/crypto's RequestSubsystem does not start the session's I/O
	// copiers (unlike Shell/Start), so assigned Stdin/Stdout would be
	// ignored and Wait would fail — wire pipes by hand instead.
	stdin, err := cs.StdinPipe()
	if err != nil {
		sess.Exit(1)
		return
	}
	stdout, err := cs.StdoutPipe()
	if err != nil {
		sess.Exit(1)
		return
	}
	stderr, err := cs.StderrPipe()
	if err != nil {
		sess.Exit(1)
		return
	}
	if err := cs.RequestSubsystem(subsystem); err != nil {
		log.Printf("sshgate: subsystem %s → %s: request: %v", subsystem, vmName, err)
		fmt.Fprintf(sess.Stderr(), "shed: subsystem %s: %v\r\n", subsystem, err)
		sess.Exit(1)
		return
	}
	go func() {
		io.Copy(stdin, sess)
		stdin.Close()
	}()
	go io.Copy(sess.Stderr(), stderr)
	io.Copy(sess, stdout)
	sess.Exit(0)
}

// handleDirectTCPIP implements `ssh -L` through the gateway: the forwarded
// destination is resolved inside the target VM.
func (s *Server) handleDirectTCPIP(srv *gliderssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx gliderssh.Context) {
	vmName := ctx.User()
	if ControlUsers[vmName] {
		newChan.Reject(gossh.Prohibited, "port forwarding works on vm sessions (ssh -L ... <vm>@shed)")
		return
	}
	var p struct {
		DestAddr   string
		DestPort   uint32
		OriginAddr string
		OriginPort uint32
	}
	if err := gossh.Unmarshal(newChan.ExtraData(), &p); err != nil {
		newChan.Reject(gossh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}
	run, err := s.Mgr.EnsureRunning(ctx, vmName)
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	guest, err := run.DialGuest(ctx, int(p.DestPort))
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, fmt.Sprintf("dial %s:%d in vm: %v", p.DestAddr, p.DestPort, err))
		return
	}
	ch, reqs, err := newChan.Accept()
	if err != nil {
		guest.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	go func() {
		defer ch.Close()
		defer guest.Close()
		io.Copy(ch, guest)
	}()
	io.Copy(guest, ch)
	ch.Close()
	guest.Close()
}

func splitEnv(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}
