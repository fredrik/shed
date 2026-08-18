package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredrik/shed/internal/backend/stubbackend"
	"github.com/fredrik/shed/internal/config"
	"github.com/fredrik/shed/internal/httpgate"
	"github.com/fredrik/shed/internal/store"
	"github.com/fredrik/shed/internal/vm"
	"github.com/fredrik/shed/internal/vm/vmspec"
)

type fakeSession struct {
	cmd string
	in  bytes.Buffer
	out bytes.Buffer
	err bytes.Buffer
}

func (f *fakeSession) RawCommand() string          { return f.cmd }
func (f *fakeSession) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeSession) Write(p []byte) (int, error) { return f.out.Write(p) }
func (f *fakeSession) Stderr() io.ReadWriter       { return &f.err }

type fakePrep struct{}

func (fakePrep) EnsureImage(ctx context.Context, ref string) (vmspec.ImageInfo, string, error) {
	return vmspec.ImageInfo{Digest: "sha256:fake", ExposedPorts: []int{80}}, "/dev/null", nil
}
func (fakePrep) EnsureDataDisk(path string, sizeGB int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("disk"), 0o644)
}

func newDeps(t *testing.T) Deps {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg := &config.Config{
		SSHAddr: "127.0.0.1:2222", HTTPAddr: "127.0.0.1:8080",
		DefaultImage: "test:latest", DefaultCPUs: 1, DefaultMemoryMB: 256, DefaultDiskGB: 1,
		Pool: config.Pool{CPUs: 8, MemoryMB: 8192, DiskGB: 50},
	}
	mgr := vm.NewManager(cfg, st, stubbackend.New(), fakePrep{}, "", func() []string { return nil })
	if err := mgr.Recover(); err != nil {
		t.Fatal(err)
	}
	gate := &httpgate.Server{Addr: cfg.HTTPAddr, Suffix: "shed.localhost", Mgr: mgr}
	if err := gate.EnsureSecret(t.TempDir() + "/secret"); err != nil {
		t.Fatal(err)
	}
	return Deps{Mgr: mgr, Cfg: cfg, Gate: gate, AuthorizedKeysPath: t.TempDir() + "/ak", User: "tester"}
}

func run(t *testing.T, deps Deps, cmd string) (int, string, string) {
	t.Helper()
	sess := &fakeSession{cmd: cmd}
	code := Run(sess, deps)
	return code, sess.out.String(), sess.err.String()
}

func TestNewLsRmFlow(t *testing.T) {
	deps := newDeps(t)

	code, out, errOut := run(t, deps, "new box --image alpine:latest")
	if code != 0 {
		t.Fatalf("new failed: %s / %s", out, errOut)
	}
	if !strings.Contains(out, "ssh box@shed") || !strings.Contains(out, "http://box.shed.localhost:8080") {
		t.Fatalf("new output missing hints:\n%s", out)
	}

	code, out, _ = run(t, deps, "ls")
	if code != 0 || !strings.Contains(out, "box") || !strings.Contains(out, "running") {
		t.Fatalf("ls: %s", out)
	}

	code, out, _ = run(t, deps, "ls --json")
	if code != 0 {
		t.Fatal("ls --json failed")
	}
	var recs []vmspec.VM
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("ls --json not parseable: %v\n%s", err, out)
	}
	if len(recs) != 1 || recs[0].Spec.Name != "box" {
		t.Fatalf("ls --json: %+v", recs)
	}

	code, _, errOut = run(t, deps, "rm box")
	if code != 0 {
		t.Fatalf("rm: %s", errOut)
	}
	_, out, _ = run(t, deps, "ls --json")
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("ls after rm: %s", out)
	}
}

func TestInvalidCommandFails(t *testing.T) {
	deps := newDeps(t)
	code, _, errOut := run(t, deps, "definitely-not-a-command")
	if code == 0 {
		t.Fatal("unknown command should fail")
	}
	if errOut == "" {
		t.Fatal("expected error output")
	}
}

func TestEmptyCommandShowsHelp(t *testing.T) {
	deps := newDeps(t)
	code, out, _ := run(t, deps, "")
	if code != 0 || !strings.Contains(out, "Available Commands") {
		t.Fatalf("help: code=%d out=%s", code, out)
	}
}

func TestShareFlow(t *testing.T) {
	deps := newDeps(t)
	run(t, deps, "new web --no-start")

	code, out, _ := run(t, deps, "share web")
	if code != 0 || !strings.Contains(out, "shed_token=") {
		t.Fatalf("share: %s", out)
	}
	code, out, _ = run(t, deps, "share set-public web")
	if code != 0 || !strings.Contains(out, "public") {
		t.Fatalf("set-public: %s", out)
	}
	code, out, _ = run(t, deps, "share port web 3000")
	if code != 0 || !strings.Contains(out, "3000") {
		t.Fatalf("share port: %s", out)
	}
	code, out, _ = run(t, deps, "share ls web")
	if code != 0 || !strings.Contains(out, "public") || !strings.Contains(out, "3000") {
		t.Fatalf("share ls: %s", out)
	}
}

func TestQuotedArgumentsParse(t *testing.T) {
	deps := newDeps(t)
	code, _, errOut := run(t, deps, `ssh-key add "not a real key"`)
	if code == 0 {
		t.Fatalf("bogus key accepted: %s", errOut)
	}
	if !strings.Contains(errOut, "not a valid public key") {
		t.Fatalf("unexpected error: %s", errOut)
	}
}
