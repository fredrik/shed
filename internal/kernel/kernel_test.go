package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// serve returns a server handing out image at /Image, counting requests.
func serve(t *testing.T, image []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/Image" {
			http.NotFound(w, r)
			return
		}
		w.Write(image)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestEnsureDownloadsAndVerifies(t *testing.T) {
	image := []byte("not really a kernel")
	srv, hits := serve(t, image)
	dest := filepath.Join(t.TempDir(), "kernel", "v1", "Image")

	got, err := ensure(dest, srv.URL+"/Image", sum(image))
	if err != nil {
		t.Fatal(err)
	}
	if got != dest {
		t.Fatalf("path = %q, want %q", got, dest)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(image) {
		t.Fatalf("cached content = %q", b)
	}

	// A second call finds the verified cache and never touches the network.
	if _, err := ensure(dest, srv.URL+"/Image", sum(image)); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestEnsureRejectsChecksumMismatch(t *testing.T) {
	srv, _ := serve(t, []byte("tampered"))
	dest := filepath.Join(t.TempDir(), "Image")

	_, err := ensure(dest, srv.URL+"/Image", sum([]byte("expected")))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("a rejected download must not be left at the cache path")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".kernel-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestEnsureReportsHTTPErrors(t *testing.T) {
	srv, _ := serve(t, nil)
	dest := filepath.Join(t.TempDir(), "Image")

	_, err := ensure(dest, srv.URL+"/missing", "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want a 404", err)
	}
}

func TestEnsureReplacesCorruptCache(t *testing.T) {
	image := []byte("good kernel")
	srv, hits := serve(t, image)
	dest := filepath.Join(t.TempDir(), "Image")
	if err := os.WriteFile(dest, []byte("bit rot"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ensure(dest, srv.URL+"/Image", sum(image)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(image) {
		t.Fatalf("cache not replaced: %q", b)
	}
	if hits.Load() != 1 {
		t.Fatal("corrupt cache should trigger exactly one download")
	}
}

func TestSHEDKernelOverridesThePin(t *testing.T) {
	own := filepath.Join(t.TempDir(), "Image")
	if err := os.WriteFile(own, []byte("my kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHED_KERNEL", own)

	got, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != own {
		t.Fatalf("path = %q, want %q", got, own)
	}
}

func TestSHEDKernelMustExist(t *testing.T) {
	t.Setenv("SHED_KERNEL", filepath.Join(t.TempDir(), "typo"))

	if _, err := Ensure(t.TempDir()); err == nil || !strings.Contains(err.Error(), "SHED_KERNEL") {
		t.Fatalf("err = %v, want an error naming SHED_KERNEL", err)
	}
}

func TestPinnedURLNamesTheVersion(t *testing.T) {
	if !strings.Contains(url, "/kernel-"+Version+"/") {
		t.Fatalf("url %q does not point at the kernel-%s release", url, Version)
	}
	if len(imageSHA256) != 64 {
		t.Fatalf("imageSHA256 %q is not a hex SHA-256", imageSHA256)
	}
}
