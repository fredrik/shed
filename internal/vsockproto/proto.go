// Package vsockproto defines the JSON-lines protocol spoken between the
// host daemon and the exeguest agent over virtio-vsock.
//
// Handshake: the guest dials the host (CID 2) on Port after assembling its
// root, sends hello, receives its Config, applies it (hostname, network,
// authorized keys, sshd, workload), then sends ready and serves control
// messages until shutdown.
package vsockproto

// Port is the host-side vsock port the guest dials for control.
const Port = 2048

// ForwardPort is the guest-side vsock port on which the agent accepts
// port-forward connections: the host sends one ForwardHeader line, then the
// stream is spliced to 127.0.0.1:<port> inside the guest. This is the
// transport of last resort when macOS Local Network privacy blocks direct
// TCP to the guest.
const ForwardPort = 1024

// HostCID is the well-known vsock context ID of the host.
const HostCID = 2

type MessageType string

const (
	// Guest → host.
	TypeHello MessageType = "hello"
	TypeReady MessageType = "ready"
	TypeError MessageType = "error"

	// Host → guest.
	TypeConfig   MessageType = "config"
	TypeShutdown MessageType = "shutdown"
)

type Message struct {
	Type     MessageType `json:"type"`
	IP       string      `json:"ip,omitempty"`
	Hostname string      `json:"hostname,omitempty"`
	Error    string      `json:"error,omitempty"`
	Config   *Config     `json:"config,omitempty"`
}

// Config is everything per-VM the guest needs at boot. It travels over
// vsock rather than the kernel cmdline so base disks stay cache-pure.
type Config struct {
	Hostname       string   `json:"hostname"`
	AuthorizedKeys []string `json:"authorized_keys"`
	Entrypoint     []string `json:"entrypoint,omitempty"`
	Cmd            []string `json:"cmd,omitempty"`
	Env            []string `json:"env,omitempty"`
	WorkingDir     string   `json:"working_dir,omitempty"`
}

// ForwardHeader is the first JSON line on a ForwardPort connection.
type ForwardHeader struct {
	Port int `json:"port"`
}
