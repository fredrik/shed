# SSH gateway

The gateway is the daemon's front door. Everything a user does with shed
goes through it: control commands, interactive shells, scp/sftp, and
local port forwarding. Package: `internal/sshgate`, built on
`gliderlabs/ssh` (server side) and `golang.org/x/crypto/ssh` (client
side, for brokering).

## Two listeners, one handler

`sshgate.Server` runs two `gliderlabs/ssh.Server` values with identical
handlers:

| Listener | Address | Client auth |
|----------|---------|-------------|
| TCP | `127.0.0.1:2222` (config `ssh_addr`) | Public key against `<state>/authorized_keys` |
| Unix socket | `<state>/control.sock`, chmod 0600 | None (gliderlabs sets `NoClientAuth` when a server has no auth handlers) |

They must be two server values rather than two listeners on one server
because auth configuration is per server. A stale socket file from a
previous run is removed before listening. Both share the same host key
(`keys/host_ed25519`, generated on first run).

Authentication on TCP re-reads `authorized_keys` on every attempt, so
`ssh-key add` takes effect immediately without a restart. Rejected keys
are logged with their SHA-256 fingerprint and the requested username.

## Routing by username

This is the sshpiper idea, hand-rolled:

- Username in `ControlUsers` (currently only `shed`) → `control.Run`
  with the session; the session exits with the command's code.
- Any other username → the name of a VM. The session is brokered into
  that VM's sshd.

The same rule applies to the sftp subsystem and to `direct-tcpip`
channels: on the control user they are refused with a message
explaining they work on VM sessions.

## Brokering a session into a VM

`connectVM` is the shared entry point for shells, exec, sftp. It:

1. Looks the VM up. If it does not exist, validates the name and calls
   `Manager.Create` with default options (image, cpu, memory, disk from
   config) and a 10 min context, writing `shed: creating vm <name>...`
   and any pull or bake progress to the session's **stderr**, with `\n`
   rewritten to `\r\n` because the client tty is raw at this point. If
   Create fails but the VM now exists (a concurrent session won the
   race), it falls through.
2. Calls `Manager.EnsureRunning` with a 90 s context. This boots a
   stopped VM on demand; a running one returns immediately.
3. Calls `RunningVM.DialGuest(22)` to get a byte stream to the guest's
   sshd.
4. Runs an ssh client handshake over that stream as user `root`, with
   the daemon's broker key (`keys/broker_ed25519`) as the only auth
   method, host key verification disabled, 15 s timeout.

The broker key's public half is appended to every guest's authorized
keys at boot (see [guest-agent.md](guest-agent.md)), which is what makes
step 4 succeed on any image. The guest's host key is generated fresh on
every boot and is not checked: trust comes from the transport (the
daemon dialing a VM it booted itself, over NAT or vsock), not from key
continuity.

### Shell and exec (`brokerSession`)

With a client connection in hand, the gateway opens one ssh session on
it and mirrors the incoming session onto it:

- Every `env` request the client sent is forwarded with `Setenv`
  (best effort; the guest may ignore them).
- If the client requested a pty, the same terminal type and window size
  are requested from the guest, and window-change events are relayed
  for the life of the session. Terminal modes are not forwarded.
- stdin, stdout and stderr are wired straight through.
- The raw exec line, if any, is passed unchanged (`Start`); otherwise a
  shell is requested (`Shell`).
- The guest's exit status is propagated. A missing exit status or any
  other error maps to 1.

Nothing is interpreted in between: the gateway does not know or care
which user the guest will run the session as. That decision is made
guest-side from the `User` field of the boot config.

### sftp and scp (`brokerSubsystem`)

The `sftp` subsystem handler connects the same way and requests the
`sftp` subsystem on the guest session. x/crypto's `RequestSubsystem`
does not start the I/O copiers that `Shell` and `Start` do, so the
handler wires stdin, stdout and stderr pipes by hand. Modern OpenSSH
(9+) runs `scp` over sftp, so this one handler covers both. The guest
serves sftp as the session user, so ownership comes out right.

### Local port forwarding (`handleDirectTCPIP`)

`ssh -L 8080:localhost:80 web@shed` arrives as a `direct-tcpip` channel
on a connection whose username is `web`. The handler decodes the
channel payload, calls `EnsureRunning`, and opens `DialGuest(DestPort)`.
The destination **host** in the payload is ignored: the port is always
resolved inside the target VM, on whatever address `DialGuest` uses
(the guest IP over NAT, or guest loopback over the vsock forwarder).
Bytes are spliced both ways until either side closes. `ssh -R` and
agent forwarding are not implemented; the requests are not accepted.

## Error surface

Every failure on a VM session is written to the client's stderr as
`shed: <message>\r\n` and the session exits 1. Pool exhaustion, invalid
names, boot timeouts and dial failures all surface this way, so a user
who types `ssh typo@shed` gets a one-line explanation rather than a
hang.

## Design notes

**Why broker as an ssh client instead of splicing TCP to the guest's
sshd.** Terminating the client's ssh at the gateway lets the daemon
authenticate the user itself, create the VM on first connect, boot it
on demand, print progress before the guest exists, and implement `-L`
and sftp without the guest ever seeing the user's keys. The cost is a
second ssh hop and the loss of a few niceties (agent forwarding,
terminal modes) until they are explicitly relayed.

**Why a unix socket speaking ssh.** See
[control-plane.md](control-plane.md): one handler, one command tree, no
drift, and no key management for the local client.

## Spec notes

- Control usernames: `{"shed"}`. All other usernames are VM names and
  are validated with `vmspec.ValidName` before any VM is created.
- TCP auth: OpenSSH public-key auth; the key must appear in
  `<state>/authorized_keys`, re-read per attempt. No password or
  keyboard-interactive.
- Socket: `<state>/control.sock`, mode 0600, no client auth, same host
  key as TCP.
- Create-on-connect: default options, 10 min timeout, progress to
  stderr with CRLF line endings.
- EnsureRunning timeout for brokered sessions: 90 s. Broker ssh
  handshake timeout: 15 s.
- Broker login: user `root`, auth = daemon broker key (ed25519), host
  key ignored.
- Relayed from client to guest: env requests, pty request (term, rows,
  cols), window changes, exec line or shell request, stdin/stdout/
  stderr, exit status. Not relayed: terminal modes, signals, agent
  forwarding, `-R`.
- Subsystems: only `sftp`, refused on the control user.
- `direct-tcpip`: destination host ignored, destination port dialed via
  `DialGuest`; refused on the control user.
- Error messages to the client: `shed: <msg>\r\n` on stderr, exit 1.
