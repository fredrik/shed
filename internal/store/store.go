// Package store owns the daemon's state directory: one JSON document per
// VM, written atomically, plus an exclusive lock so only one shedd runs.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/fredrik/shed/internal/vm/vmspec"
)

type Store struct {
	Root   string
	lockFd int
}

// Open creates the state directory if needed and takes the daemon lock.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "vms"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "keys"), 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(root, "shedd.lock"), unix.O_CREAT|unix.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("another shedd is already running (lock %s held)", filepath.Join(root, "shedd.lock"))
	}
	return &Store{Root: root, lockFd: fd}, nil
}

func (s *Store) Close() {
	if s.lockFd > 0 {
		unix.Close(s.lockFd)
	}
}

func (s *Store) VMDir(name string) string {
	return filepath.Join(s.Root, "vms", name)
}

func (s *Store) SaveVM(rec *vmspec.VM) error {
	dir := s.VMDir(rec.Spec.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".vm-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "vm.json"))
}

func (s *Store) LoadVMs() ([]*vmspec.VM, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "vms"))
	if err != nil {
		return nil, err
	}
	var vms []*vmspec.VM
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.VMDir(e.Name()), "vm.json"))
		if err != nil {
			continue // half-created dir; ignore
		}
		var rec vmspec.VM
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("corrupt vm.json for %s: %w", e.Name(), err)
		}
		vms = append(vms, &rec)
	}
	return vms, nil
}

func (s *Store) DeleteVM(name string) error {
	return os.RemoveAll(s.VMDir(name))
}
