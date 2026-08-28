package vm

import (
	"os"
	"path/filepath"
	"testing"
)

// The image was called exeuntu until it grew its own taste. VM records
// written back then still say so, and must not be sent to a registry.
func TestIsSheduntuAcceptsTheOldName(t *testing.T) {
	for _, ref := range []string{
		"sheduntu", "sheduntu:latest",
		"exeuntu", "exeuntu:latest",
	} {
		if !isSheduntu(ref) {
			t.Errorf("isSheduntu(%q) = false, want true", ref)
		}
	}
	for _, ref := range []string{
		"ubuntu:24.04", "sheduntu:v1", "notsheduntu", "",
	} {
		if isSheduntu(ref) {
			t.Errorf("isSheduntu(%q) = true, want false", ref)
		}
	}
}

func TestPruneOldSheduntuSweepsBothNames(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	keep := write("sheduntu-aaaaaaaaaaaa.img")
	write("sheduntu-aaaaaaaaaaaa.img.json")
	stale := write("sheduntu-bbbbbbbbbbbb.img")
	staleJSON := write("sheduntu-bbbbbbbbbbbb.img.json")
	renamed := write("exeuntu-cccccccccccc.img")
	other := write("ubuntu-24.04.img")

	pruneOldSheduntu(dir, "aaaaaaaaaaaa")

	for _, path := range []string{keep, other} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s should have survived: %v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{stale, staleJSON, renamed} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s should have been pruned", filepath.Base(path))
		}
	}
}
