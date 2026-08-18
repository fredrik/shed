// Package vmspec holds the shared VM record types, dependency-free so every
// layer (store, backend, control plane) can use them.
package vmspec

import (
	"crypto/sha256"
	"fmt"
	"net"
	"regexp"
	"time"
)

type State string

const (
	StateCreating State = "creating"
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateError    State = "error"
)

// Spec is the user-requested shape of a VM.
type Spec struct {
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	CPUs      int       `json:"cpus"`
	MemoryMB  int       `json:"memory_mb"`
	DiskGB    int       `json:"disk_gb"`
	Created   time.Time `json:"created"`
	Autostart bool      `json:"autostart,omitempty"`
}

// ImageInfo is what the OCI image resolved to at create time, kept so
// starts don't re-pull.
type ImageInfo struct {
	Digest       string   `json:"digest"`
	Entrypoint   []string `json:"entrypoint,omitempty"`
	Cmd          []string `json:"cmd,omitempty"`
	Env          []string `json:"env,omitempty"`
	WorkingDir   string   `json:"working_dir,omitempty"`
	ExposedPorts []int    `json:"exposed_ports,omitempty"`
}

// Share is the HTTP front door configuration.
type Share struct {
	Port   int      `json:"port,omitempty"` // 0 = smallest exposed port
	Public bool     `json:"public,omitempty"`
	Emails []string `json:"emails,omitempty"`
}

// VM is the persisted record (vm.json).
type VM struct {
	Spec           Spec      `json:"spec"`
	Image          ImageInfo `json:"image"`
	Share          Share     `json:"share"`
	State          State     `json:"state"`
	LastStopReason string    `json:"last_stop_reason,omitempty"`
	IP             string    `json:"ip,omitempty"` // last known, valid while running
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName reports whether name is usable as a VM name; names double as
// ssh usernames and URL host labels.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid vm name %q: use lowercase letters, digits, and hyphens (max 63 chars)", name)
	}
	return nil
}

// MAC derives the VM's stable, locally administered unicast MAC from its
// name, so leases and ARP entries survive restarts and are debuggable.
func (s Spec) MAC() net.HardwareAddr {
	sum := sha256.Sum256([]byte("shed-mac:" + s.Name))
	return net.HardwareAddr{0x06, sum[0], sum[1], sum[2], sum[3], sum[4]}
}
