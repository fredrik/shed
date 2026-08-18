// Package config holds daemon configuration: defaults tuned for this
// machine, optionally overridden by config.toml in the state directory.
package config

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
	"golang.org/x/sys/unix"
)

type Pool struct {
	CPUs     int `toml:"cpus"`
	MemoryMB int `toml:"memory_mb"`
	DiskGB   int `toml:"disk_gb"`
}

type Config struct {
	SSHAddr  string `toml:"ssh_addr"`
	HTTPAddr string `toml:"http_addr"`

	DefaultImage    string `toml:"default_image"`
	DefaultCPUs     int    `toml:"default_cpus"`
	DefaultMemoryMB int    `toml:"default_memory_mb"`
	DefaultDiskGB   int    `toml:"default_disk_gb"`

	Pool Pool `toml:"pool"`

	// Derived, not configurable via TOML.
	StateDir string `toml:"-"`
	CacheDir string `toml:"-"`
}

func StateDir() string {
	if dir := os.Getenv("DEVEXE_STATE_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "devexe")
}

func Load() (*Config, error) {
	stateDir := StateDir()
	home, _ := os.UserHomeDir()

	cfg := &Config{
		SSHAddr:         "127.0.0.1:2222",
		HTTPAddr:        "127.0.0.1:8080",
		DefaultImage:    "ubuntu:24.04",
		DefaultCPUs:     2,
		DefaultMemoryMB: 1024,
		DefaultDiskGB:   10,
		Pool: Pool{
			CPUs:     max(runtime.NumCPU()-2, 2),
			MemoryMB: hostMemoryMB() / 2,
			DiskGB:   100,
		},
		StateDir: stateDir,
		CacheDir: filepath.Join(home, "Library", "Caches", "devexe"),
	}

	path := filepath.Join(stateDir, "config.toml")
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func hostMemoryMB() int {
	mem, err := unix.SysctlUint64("hw.memsize")
	if err != nil || mem == 0 {
		return 8192
	}
	return int(mem >> 20)
}
