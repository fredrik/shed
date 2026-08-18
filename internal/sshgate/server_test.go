package sshgate

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/fredrik/shed/internal/backend/stubbackend"
	"github.com/fredrik/shed/internal/config"
	"github.com/fredrik/shed/internal/control"
	"github.com/fredrik/shed/internal/httpgate"
	"github.com/fredrik/shed/internal/keys"
	"github.com/fredrik/shed/internal/store"
	"github.com/fredrik/shed/internal/vm"
	"github.com/fredrik/shed/internal/vm/vmspec"
)

type fakePrep struct{}

func (fakePrep) EnsureImage(ctx context.Context, ref string) (vmspec.ImageInfo, string, error) {
	return vmspec.ImageInfo{Digest: "sha256:fake"}, "/dev/null", nil
}
func (fakePrep) EnsureDataDisk(path string, sizeGB int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("disk"), 0o644)
}

// TestControlSocket exercises the no-auth unix socket end to end: dial,
// handshake without a client key, run a control command, get its exit code.
func TestControlSocket(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg := &config.Config{
		SSHAddr: "127.0.0.1:2222", HTTPAddr: "127.0.0.1:8080",
		DefaultImage: "test:latest", DefaultCPUs: 1, DefaultMemoryMB: 256, DefaultDiskGB: 1,
		Pool:     config.Pool{CPUs: 8, MemoryMB: 8192, DiskGB: 50},
		StateDir: dir,
	}
	mgr := vm.NewManager(cfg, st, stubbackend.New(), fakePrep{}, "", func() []string { return nil })
	if err := mgr.Recover(); err != nil {
		t.Fatal(err)
	}
	gate := &httpgate.Server{Addr: cfg.HTTPAddr, Suffix: "shed.localhost", Mgr: mgr}
	if err := gate.EnsureSecret(filepath.Join(dir, "secret")); err != nil {
		t.Fatal(err)
	}
	hostSigner, err := keys.EnsureED25519(filepath.Join(dir, "host_key"), "test-host")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		HostSigner: hostSigner,
		Mgr:        mgr,
		ControlDeps: control.Deps{
			Mgr: mgr, Cfg: cfg, Gate: gate,
			AuthorizedKeysPath: filepath.Join(dir, "ak"), User: "tester",
		},
	}
	sockPath := cfg.ControlSocket()
	go s.ServeSocket(sockPath)
	t.Cleanup(func() { s.Close() })

	var conn net.Conn
	for i := 0; ; i++ {
		conn, err = net.Dial("unix", sockPath)
		if err == nil {
			break
		}
		if i > 200 {
			t.Fatalf("socket never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fi, err := os.Stat(sockPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi.Mode(), err)
	}

	// No Auth methods: the client offers "none", which the no-auth
	// listener must accept.
	sshConn, chans, reqs, err := gossh.NewClientConn(conn, "control.sock", &gossh.ClientConfig{
		User:            "shed",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	client := gossh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	run := func(cmd string) (error, string) {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		var out bytes.Buffer
		sess.Stdout = &out
		sess.Stderr = &out
		return sess.Run(cmd), out.String()
	}

	if err, out := run("ls --json"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("ls --json: err=%v out=%q", err, out)
	}
	if err, out := run("whoami"); err != nil || !strings.Contains(out, "tester") {
		t.Fatalf("whoami: err=%v out=%q", err, out)
	}
	err, _ = run("definitely-not-a-command")
	var ee *gossh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() == 0 {
		t.Fatalf("bad command should propagate a nonzero exit code, got %v", err)
	}
}
