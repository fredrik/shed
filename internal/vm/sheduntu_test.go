package vm

import (
	"os"
	"path/filepath"
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
		"ubuntu:24.04", "exeuntu", "sheduntu:v1", "notsheduntu", "",
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
	other := write("ubuntu-24.04.img")

	pruneOldSheduntu(dir, "aaaaaaaaaaaa")

	for _, path := range []string{keep, keepJSON, other} {
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
