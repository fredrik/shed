package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSheduntu(t *testing.T) {
	for _, ref := range []string{"sheduntu", "sheduntu:latest"} {
		if !isSheduntu(ref) {
			t.Errorf("isSheduntu(%q) = false, want true", ref)
		}
	}
	// exeuntu was this image's name until it grew its own taste. Nothing
	// refers to it any more, and it is an ordinary registry ref now.
	for _, ref := range []string{
		"ubuntu:26.04", "exeuntu", "sheduntu:v1", "notsheduntu", "",
	} {
		if isSheduntu(ref) {
			t.Errorf("isSheduntu(%q) = true, want false", ref)
		}
	}
}

func TestPruneOldSheduntu(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	keep := write("sheduntu-aaaaaaaaaaaa.img")
	keepJSON := write("sheduntu-aaaaaaaaaaaa.img.json")
	stale := write("sheduntu-bbbbbbbbbbbb.img")
	staleJSON := write("sheduntu-bbbbbbbbbbbb.img.json")
	// Referenced by a VM record; superseded, but must survive.
	pinned := write("sheduntu-cccccccccccc.img")
	pinnedJSON := write("sheduntu-cccccccccccc.img.json")
	other := write("ubuntu-26.04.img")

	pruneOldSheduntu(dir, map[string]bool{"aaaaaaaaaaaa": true, "cccccccccccc": true})

	for _, path := range []string{keep, keepJSON, pinned, pinnedJSON, other} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s should have survived: %v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{stale, staleJSON} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s should have been pruned", filepath.Base(path))
		}
	}
}

func TestSheduntuScriptBakesGhosttyTerminfo(t *testing.T) {
	script := renderSheduntuScript()
	if strings.Contains(script, sheduntuTerminfoMarker) {
		t.Fatal("terminfo placeholder left unrendered in bake script")
	}
	if !strings.Contains(script, "xterm-ghostty|ghostty|Ghostty,") {
		t.Fatal("bake script does not carry the xterm-ghostty terminfo source")
	}
	if !strings.Contains(script, "tic -x") {
		t.Fatal("bake script does not compile the terminfo")
	}
	// The embedded source must be something tic accepts; a stray edit here
	// would only surface as a failed bake, minutes in.
	tic, err := exec.LookPath("tic")
	if err != nil {
		t.Skip("no tic on this host")
	}
	out := t.TempDir()
	cmd := exec.Command(tic, "-x", "-o", out, "-")
	cmd.Stdin = strings.NewReader(xtermGhosttyTerminfo)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tic rejected embedded terminfo: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(out, "78", "xterm-ghostty")); err != nil {
		if _, err2 := os.Stat(filepath.Join(out, "x", "xterm-ghostty")); err2 != nil {
			t.Fatalf("tic produced no xterm-ghostty entry: %v", err)
		}
	}
}
