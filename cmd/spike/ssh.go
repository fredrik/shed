package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/Code-Hex/vz/v3"
	gossh "golang.org/x/crypto/ssh"

	"github.com/fredrik/local-devexe/internal/vsockproto"
)

// sshRoundTrip waits for the guest's ready report on vsock, sshes to the
// guest over the NAT network, runs a command, and asks for shutdown.
func sshRoundTrip(vm *vz.VirtualMachine, start time.Time) error {
	devs := vm.SocketDevices()
	if len(devs) == 0 {
		return errors.New("no vsock device on VM")
	}
	listener, err := devs[0].Listen(vsockproto.Port)
	if err != nil {
		return fmt.Errorf("vsock listen: %w", err)
	}

	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		connCh <- c
	}()

	var conn net.Conn
	select {
	case conn = <-connCh:
	case err := <-errCh:
		return fmt.Errorf("vsock accept: %w", err)
	case <-time.After(60 * time.Second):
		return errors.New("timed out waiting for guest ready on vsock")
	}
	defer conn.Close()

	var ready vsockproto.Message
	if err := json.NewDecoder(conn).Decode(&ready); err != nil {
		return fmt.Errorf("decode ready: %w", err)
	}
	if ready.Type != vsockproto.TypeReady {
		return fmt.Errorf("unexpected first message %q", ready.Type)
	}
	log.Printf("spike: guest ready, ip=%s (t=%s)", ready.IP, time.Since(start).Round(time.Millisecond))

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}
	cfg := &gossh.ClientConfig{
		User:            "root",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	// Prefer direct TCP to the guest's NAT IP; fall back to vsock when
	// macOS Local Network privacy (TCC) blocks the bridge.
	var client *gossh.Client
	client, err = gossh.Dial("tcp", net.JoinHostPort(ready.IP, "22"), cfg)
	if err != nil {
		log.Printf("spike: tcp ssh failed (%v), falling back to vsock transport", err)
		raw, verr := devs[0].Connect(22)
		if verr != nil {
			return fmt.Errorf("vsock connect: %w (after tcp failure: %v)", verr, err)
		}
		c, chans, reqs, cerr := gossh.NewClientConn(raw, "vsock:22", cfg)
		if cerr != nil {
			return fmt.Errorf("ssh over vsock: %w", cerr)
		}
		client = gossh.NewClient(c, chans, reqs)
		log.Printf("spike: connected over vsock")
	} else {
		log.Printf("spike: connected over tcp %s:22", ready.IP)
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return fmt.Errorf("ssh session: %w", err)
	}
	out, err := sess.CombinedOutput(`uname -a && . /etc/os-release && echo "$PRETTY_NAME"`)
	client.Close()
	if err != nil {
		return fmt.Errorf("ssh exec: %w (output: %s)", err, out)
	}
	log.Printf("spike: ssh exec OK (boot→ssh %s), output:\n%s", time.Since(start).Round(time.Millisecond), out)

	if err := json.NewEncoder(conn).Encode(vsockproto.Message{Type: vsockproto.TypeShutdown}); err != nil {
		return fmt.Errorf("send shutdown: %w", err)
	}
	return nil
}
