// shed is the local control client: it sends its argv line to the shedd
// daemon over the control socket and streams the result back. The command
// surface is whatever the daemon serves — the same cobra tree behind
// `ssh shed <command>` — so this binary parses nothing and needs no keys.
// The one exception is `shed version`, which also reports the client's own
// build (see version.go).
package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/kballard/go-shellquote"
	gossh "golang.org/x/crypto/ssh"

	"github.com/fredrik/shed/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		return fail("config: %v", err)
	}
	sockPath := cfg.ControlSocket()
	if isVersionCmd(args) {
		return runVersion(args, func() (*gossh.Client, error) { return dial(sockPath) }, os.Stdout, os.Stderr)
	}
	client, err := dial(sockPath)
	if err != nil {
		return fail("%v", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fail("session: %v", err)
	}
	defer sess.Close()
	sess.Stdin = os.Stdin
	sess.Stdout = os.Stdout
	sess.Stderr = os.Stderr

	switch e := sess.Run(shellquote.Join(args...)).(type) {
	case nil:
		return 0
	case *gossh.ExitError:
		return e.ExitStatus()
	default:
		return fail("%v", e)
	}
}

// dial connects to the daemon's control socket and completes the ssh
// handshake. Trust is the 0600 socket in our own state directory; the
// daemon's host key carries no extra information here.
func dial(sockPath string) (*gossh.Client, error) {
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable at %s (start it: shedd serve)", sockPath)
	}
	sshConn, chans, reqs, err := gossh.NewClientConn(conn, sockPath, &gossh.ClientConfig{
		User:            "shed",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake: %v", err)
	}
	return gossh.NewClient(sshConn, chans, reqs), nil
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "shed: "+format+"\n", a...)
	return 1
}
