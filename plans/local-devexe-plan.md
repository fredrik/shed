# Plan: local-devexe — a local clone of exe.dev

## Context

The repo (`~/code/fredrik/local-devexe`, currently empty) exists to build a local clone of exe.dev, the SSH-based microVM service by David Crawshaw and Josh Bleecher Snyder. Goal: reproduce the exe.dev experience on this Mac — `ssh` to a control endpoint to create/manage **real Linux microVMs** booted from OCI images, ssh into them, and reach their web ports through a local HTTP front door.

### What exe.dev is (research summary)

- **SSH-first control plane**: `ssh exe.dev <command>` is the entire API. Commands: `help, doc, ls, new, rm, restart, rename, cp, share, whoami, ssh-key, shelley, browser, ssh`. Flags like `new --image alpine:latest --cpu --memory --disk --command`; `--json` for scripting. `cp` clones a VM.
- **VM model**: starts from an OCI image (default `exeuntu`, Ubuntu-derived, sshd baked in), flattened onto a block device, booted as a KVM-isolated microVM with its own kernel in ~2 s. Full systemd, root, persistent disk.
- **SSH brokering**: connections to VMs go through their edge (sshpiper); key-only auth.
- **HTTP front door**: each VM gets `vmname.exe.xyz`, TLS at the edge, forwarded to the smallest `EXPOSE`d port (or `share port`). Private by default (login redirect); `share set-public` / `share add <email>`.
- **Resource pool**: plan = pool of CPU/RAM/disk shared across VMs; stopped VMs keep disk, release CPU/RAM.
- **Shelley** (their web coding agent): out of scope; optional stretch = run `claude` inside a VM.

### Target environment (verified)

macOS 15.7.3 Sequoia, Apple Silicon arm64, Go 1.25.6, Homebrew. Single user, local only. `Code-Hex/vz/v3` v3.7.1 and `gliderlabs/ssh` v0.3.8 current; no Docker daemon required at runtime.

## Architecture at a glance

One Go daemon (`exed`) embedding: an SSH gateway on 127.0.0.1:2222 (control plane + broker into VMs), a VM manager over Apple Virtualization.framework (via `Code-Hex/vz/v3`), an OCI→disk pipeline, and an HTTP front door on 127.0.0.1:8080. A static Go guest agent (`exeguest`) is pid1-bootstrapper + sshd + supervisor inside every VM. Naming: CLI/ssh alias `devexe`, module `github.com/fredrik/local-devexe`, HTTP suffix `*.exe.localhost`.

## Repo layout

```
go.mod  Makefile  vz.entitlements             # com.apple.security.virtualization only
cmd/exed/            # daemon: `exed serve|install|doctor`
cmd/exeguest/        # guest agent: GOOS=linux GOARCH=arm64 CGO=0, go:embed'ed into exed
cmd/spike/           # throwaway de-risk program (M0), deleted later
cmd/codesign/        # `go test -exec` signing wrapper (Code-Hex/vz's pattern)
internal/
  config/            # TOML config + defaults (pool, ports, paths)
  store/             # state dir, atomic JSON persistence (temp+rename), flock
  vm/                # Manager: registry, lifecycle state machine, pool; vm/vmspec/ shared types
  backend/           # Backend interface; backend/vzbackend/; backend/stubbackend/ (M1+tests)
  image/             # pull (go-containerregistry) + flatten (mutate.Extract) + OCI layout cache
  diskfs/            # tar2ext4 base disk, mke2fs data disk, offline resize2fs
  kernel/            # pinned Kata static arm64 kernel: download, sha256-verify, cache
  initramfs/         # Go-generated cpio.gz wrapping exeguest as /init
  sshgate/           # gliderlabs/ssh server: auth, username routing, session broker
  control/           # cobra command tree (new/ls/rm/share/...) bound to ssh session
  httpgate/          # reverse proxy front door, Host routing, auth gate
  vsockproto/        # host↔guest JSON-lines protocol (shared with exeguest)
  keys/              # host key, broker key, authorized_keys
testdata/            # testscript golden files, fixtures
```

**State dir** `~/.local/share/devexe/` (`DEVEXE_STATE_DIR` override): `config.toml`, `authorized_keys`, `keys/{host,broker}_ed25519`, `exed.lock`, `vms/<name>/{vm.json,data.img,serial.log}`. Caches in `~/Library/Caches/devexe/`: `oci/` (image layouts by digest), `base/<digest>.img` (shared RO base disks), `kernel/<ver>/Image`. Persistence = one JSON per VM, atomic writes; no SQLite.

## Core interfaces

```go
// internal/backend/backend.go
type Backend interface {
    Name() string
    Validate(spec vmspec.Spec) error
    Start(ctx context.Context, req StartRequest) (RunningVM, error)
}
type StartRequest struct {
    Spec          vmspec.Spec  // name, cpus, memMB, deterministic MAC
    BaseDiskPath  string       // RO ext4 from image (vda)
    DataDiskPath  string       // RW ext4, per-VM (vdb)
    KernelPath    string
    InitramfsPath string
    SerialLogPath string
    GuestConfig   vsockproto.Config // hostname, authorized_keys, entrypoint/cmd/env, ports
}
type RunningVM interface {
    Shutdown(ctx context.Context) error       // graceful via vsock; timeout → Kill
    Kill() error
    Done() <-chan struct{}
    GuestIP(ctx context.Context) (netip.Addr, error)
    DialGuest(ctx context.Context, port int) (net.Conn, error) // the swappability seam
}
```

`sshgate`/`httpgate` only ever call `DialGuest` — a qemu/docker fallback backend stays drop-in. In vzbackend, `DialGuest` prefers TCP to the NAT IP and transparently falls back to a vsock-forwarded guest-loopback connection (sidesteps macOS 15 Local Network TCC).

```go
// internal/image + internal/diskfs
Pull(ctx, ref) (Image, error)        // remote.Image, linux/arm64, keychain auth, layout cache
Flatten(img) io.ReadCloser           // mutate.Extract — whiteouts applied, pure tar stream
BuildBaseDisk(tar, dest) error       // tar2ext4: RO ext4, uid/gid/xattrs/devnodes from tar headers
NewDataDisk(dest, sizeGB) error      // truncate (APFS-sparse) + brew mke2fs -E root_owner=0:0
```

The rootfs never touches APFS as files — tar streams straight into a userspace ext4 writer, so no root needed and full ownership fidelity (same architecture as Apple's containerization).

## Boot design (two disks + overlay)

- **Base disk**: `tar2ext4` (github.com/Microsoft/hcsshim/ext4/tar2ext4, pure Go) output is intentionally read-only ext4 — used as overlay lowerdir, cached per image digest, shared across VMs (APFS `clonefile` per-VM copy if concurrent RO attach is refused — spike confirms).
- **Data disk**: per-VM sparse raw + `mke2fs` (brew e2fsprogs, keg-only path `/opt/homebrew/opt/e2fsprogs/sbin`) — the only external tool dependency. Growth: stop, `truncate` larger, offline `resize2fs`. "10 GB disk from 200 MB image" costs ~MBs of real APFS space.
- **Kernel**: Kata Containers static arm64 `vmlinux.container` (the kernel Apple's own `container` stack direct-boots): monolithic, no modules, virtio blk/net/console/vsock + ext4 + overlayfs built in. `VZLinuxBootLoader` on arm64 requires an *uncompressed* Image (rules out Alpine/Ubuntu vmlinuz, whose virtio is modular anyway). Pinned by sha256, downloaded once. Self-build recipe (apple/containerization `config-arm64`) as escape hatch.
- **initramfs**: Go-generated newc cpio.gz containing exactly `/init` = `exeguest`. Cmdline: `console=hvc0 rdinit=/init` — all per-VM config (hostname, keys, entrypoint) is delivered over vsock, keeping base disks cache-pure.
- **cp (clone)**: stop-or-quiesce source, APFS `clonefile` data.img, share base disk, new identity/MAC — instant.
- Raw disks only (vz supports nothing else); `DiskImageCachingModeAutomatic` + `SynchronizationModeFsync`. Expected cold boot → interactive ssh: **~1–2.5 s**.

## Guest agent (`exeguest`)

- **Stage 1 (initramfs pid1)**: mount proc/sys/devtmpfs, mount vda ro + vdb rw, create upper/work on first boot, mount overlayfs at /newroot, switch_root.
- **Stage 2**: dial host vsock (CID 2), receive JSON config; sethostname; eth0 up via pure-Go DHCP (`insomniacslk/dhcp` + `vishvananda/netlink`); write authorized_keys (user keys + broker key).
- **sshd**: embeds a Go SSH server (`gliderlabs/ssh` + `pkg/sftp` + `creack/pty`) bound to :22 *unless* the image ships a usable sshd — makes even alpine/distroless images ssh-able with zero image contract.
- **Handoff**: image has systemd → inject an `exeguest.service` unit and exec systemd as pid1; otherwise agent stays pid1 (zombie reaping, ENTRYPOINT/CMD supervision, signal forwarding).
- Reports `ready{ip,hostname}` over vsock; answers health/shutdown; vsock-forwards loopback ports on request (TCC-proof transport).

## SSH control plane + broker

- **Server**: gliderlabs/ssh on 127.0.0.1:2222, host key from state dir, `PublicKeyHandler` against `authorized_keys` (seeded by `exed install` from `~/.ssh/*.pub`). Key-only.
- **Username routing** (sshpiper's idea, hand-rolled): user `exe` → control plane; any other username = VM name → brokered session (auto-start if stopped; clear error if pool exhausted).
- **Control plane**: exec line → `kballard/go-shellquote` → `spf13/cobra` bound to the session (SetArgs/SetIn/SetOut/SetErr; exit code from RunE). No exec line → help. Commands: `help doc ls new rm start stop restart rename cp share whoami ssh-key ssh browser` (+ `shelley` stub). `--json` everywhere from day one via a shared table-or-JSON printer.
- **Broker**: daemon acts as SSH client (`x/crypto/ssh`, broker key) to the guest sshd over `DialGuest(22)`. Forward pty-req/window-change/env/shell/exec/signal + exit-status/exit-signal; `sftp` subsystem forwarded (covers modern scp); `direct-tcpip` (`ssh -L`) spliced via `DialGuest`. `ssh -R`/agent-forwarding deferred to M5; unsupported requests fail loudly, never hang.

## Lifecycle, pool, crash recovery

- States: `creating → stopped → starting → running → stopping → stopped`, plus `error`, `deleting`. `Manager` with registry lock + per-VM locks; per-VM watcher on `RunningVM.Done()` → transition + release pool.
- Pool from config (defaults: cores−2 CPUs, half RAM, 100 GB disk). CPU/RAM reserved at start, released at stop; disk at create/rm. Counters derived from the registry, never persisted. Friendly `pool exhausted: need 2 CPU, 1 free` errors; `ls -l` shows the pool line.
- vz VMs die with the daemon — accepted. On startup: flock, load vm.json, demote stale running states to `stopped` (`last_stop_reason: "daemon restart"`), start `autostart` VMs.

## HTTP front door

- `httputil.ReverseProxy` on 127.0.0.1:8080; Host routing `<vm>.exe.localhost` → `DialGuest(targetPort)` via custom DialContext. Browsers resolve `*.localhost` to loopback; document `curl --resolve` fallback.
- Target port: smallest EXPOSEd TCP port from image config, override via `share port <vm> <port>`.
- Private by default: HMAC-signed cookie; unauthenticated → login page explaining `ssh devexe share <vm>`; `share <vm>` prints a signed-token URL. `share set-public` disables the gate; `share add <email>` stored for parity. Stopped VM → branded 502 with the start command. Local-CA TLS: M5 stretch.

## Client ergonomics

- `exed install` (idempotent): state dir + keys; seed authorized_keys; write `~/.ssh/devexe_config` (Host devexe → 127.0.0.1:2222, User exe, own known_hosts, accept-new) + `Include` line in `~/.ssh/config`; print suggested `alias devexe='ssh devexe --'`.
- Daemon runs foreground (`exed serve`) by default; `install --launchd` writes a **per-user LaunchAgent** (KeepAlive) — must be a LaunchAgent, not LaunchDaemon: Virtualization.framework on macOS 15 needs the Aqua session/unlocked keychain.
- `exed doctor`: checks binary signature/entitlement, ports free, kernel cached, e2fsprogs installed, vz available, Local Network TCC status.
- **Signing**: Makefile builds then `codesign --entitlements vz.entitlements -f -s - bin/exed`. `vz.entitlements` = `com.apple.security.virtualization` only (NAT + vsock need nothing more; `com.apple.vm.networking` is bridged-only/Apple-restricted).

## Milestones (each demoable)

| # | Scope | Demo |
|---|-------|------|
| **M0 — spike** | `cmd/spike`: ① boot Kata kernel + hello-world initramfs, signed ② alpine tar → tar2ext4 base + mke2fs data → overlay → `/bin/sh` on console ③ vsock handshake + DHCP + agent sshd; measure boot→ssh latency. Also settle: `mke2fs -d rootfs.tar` on macOS, concurrent RO base attach. | `go run`-style script boots alpine to a root shell in seconds; go/no-go on all risky bets. |
| **M1** | Skeleton, `exed serve`+`install`, sshgate auth, cobra control plane, stubbackend, JSON persistence. `help whoami ls new rm ssh-key`, `--json`. | `ssh devexe new --name foo --image alpine:latest && ssh devexe ls -l --json` — fake VMs, real UX. |
| **M2** | vzbackend (from spike) + image/diskfs/kernel/initramfs packages + exeguest; broker shell/exec/PTY/exit codes. | `ssh foo@devexe` lands in a real ~2 s microVM; `ssh foo@devexe uname -a; echo $?`. |
| **M3** | `start/stop/restart`, watcher, startup reconcile, pool accounting, systemd handoff (ubuntu:24.04). | Kill daemon → restart → `ls` truthful; `new` until `pool exhausted`; `new --image nginx:latest` serves. |
| **M4** | httpgate + `share` (port/set-public/add/link), login gate, 502 page; sftp/scp + `-L` passthrough. | `open http://foo.exe.localhost:8080` → login redirect → share link works; `scp` a file in. |
| **M5** | `cp` (clonefile), `rename`, `doc`, `browser`, launchd, `ssh -R`, builder-VM fallback path, optional "exeuntu" default image + local-CA TLS, wake-on-HTTP. | Full parity walkthrough script. |

## Testing

- Unit (`go test ./...`, no signing/network): control commands vs in-process Manager + stubbackend; state-machine tables; pool property tests; argv edges; initramfs/cpio and tar-handling round-trips.
- Golden CLI: `rogpeppe/go-internal/testscript` txtar scripts as UX specs; `-update` regenerates.
- SSH e2e (hermetic): in-process sshgate on port 0 driven by `x/crypto/ssh` client against stubbackend (fake sshd behind DialGuest).
- vz integration (`make integration`, build-tag + env gate): run via `go test -exec "go run ./cmd/codesign"` (signs ephemeral test binaries — Code-Hex/vz's own pattern) or against the signed `bin/exed`: boot, exec, stop, reconcile-after-kill, HTTP roundtrip. Local-only, not default.

## Key risks

1. **Kata kernel not accepted by VZLinuxBootLoader** (low; apple/container precedent) → spike hour 1 verifies; fallbacks: gunzipped Ubuntu Image + our initramfs, or self-build via containerization config.
2. **tar2ext4 edge cases** (out-of-order tars, >16 GB, exotic xattrs) → pre-validate/repair tar ordering in-stream; M5 builder-VM fallback (boot agent in "builder mode", untar over vsock as real root) handles anything.
3. **macOS 15 Local Network TCC blocks host→guest TCP** on bridge100, silently → primary mitigation designed-in: vsock-forwarded loopback transport in `DialGuest`; `doctor` detects; document one-time grant.
4. **IP discovery** — macOS 15 `dhcpd_leases` often records a DUID, not the MAC → vsock agent report is primary; deterministic MACs (`0x06` + sha256(vmID)[0:5]) + ARP scan only as fallback.
5. **systemd handoff friction** (networkd fights, sshd arbitration) → agent-owns-network rule + explicit sshd arbitration; tested golden-path image; agent-as-pid1 always works meanwhile.
6. **codesign/entitlement friction** (`go run`/plain `go test` can't boot VMs) → single Makefile path, test-exec signing wrapper, `doctor` check.
7. **Daemon death kills VMs** → accepted; reconcile-on-start + autostart + launchd KeepAlive.

## Verification

- M0: spike script demonstrates image→boot→ssh end-to-end with measured latency.
- M1+: `make build && bin/exed serve`; scripted `ssh devexe …` walkthrough per milestone's demo column.
- Continuous: `go test ./...` green without entitlements/network; `make integration` on this Mac boots real VMs.

## Sources

exe.dev [homepage](https://exe.dev)/[VPS page](https://exe.dev/vps)/[docs](https://exe.dev/docs/cli-ssh), [crabbox exe-dev provider notes](https://github.com/openclaw/crabbox/blob/main/docs/providers/exe-dev.md), [Lobsters thread](https://lobste.rs/s/fpeeq0/meet_exe_dev_modern_vms), [Amplify on exe.dev](https://www.amplifypartners.com/blog-posts/exe-dev-and-the-perfect-little-computer), [Code-Hex/vz](https://github.com/Code-Hex/vz), [apple/containerization](https://github.com/apple/containerization), [tar2ext4](https://pkg.go.dev/github.com/Microsoft/hcsshim/ext4/tar2ext4), [vfkit kernel-format docs](https://github.com/crc-org/vfkit/blob/main/doc/usage.md), [vfkit #242 (dhcpd_leases DUID)](https://github.com/crc-org/vfkit/issues/242), [Sequoia Local Network TCC](https://www.rogue-research.com/2025/05/local-network-access-on-macos-15-sequoia/), [Kata releases](https://github.com/kata-containers/kata-containers/releases).
