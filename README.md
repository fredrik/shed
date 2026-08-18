# local-devexe

A local clone of [exe.dev](https://exe.dev) — the ssh-based microVM service — for one
Mac. Real Linux microVMs booted from OCI images in about a second, managed
entirely over ssh, with persistent disks and an HTTP front door.

```
$ ssh devexe new box
creating box from exeuntu...
vm box is running
  ssh box@devexe
  http://box.exe.localhost:8080

$ ssh box@devexe

  exeuntu -- Ubuntu 24.04, devexe build

dev@box:~$ echo it is a real vm with a real kernel > notes
```

The default image is **exeuntu** (in the spirit of exe.dev's): Ubuntu 24.04
with git, curl, vim, tmux, htop, ripgrep, jq and friends preinstalled, a
`dev` login user with passwordless sudo, and a real toolchain: `mise` and
`uv` system-wide in /usr/local/bin, node 24 pre-warmed via mise, and
python 3.14 (uv-managed) as dev's default alongside the apt `python3`
baseline. It is baked locally the first time it's used — a throwaway VM
boots upstream `ubuntu:24.04`, runs the recipe, and streams its rootfs
back into a cached base image (a minute or so). Upstream digest changes or
recipe edits rebake on next use, and superseded bakes are pruned. Any OCI
image still works via `--image`.

## How it works

One daemon (`exed`) runs three things:

- **SSH gateway** (127.0.0.1:2222). Routing is by username, sshpiper-style:
  user `exe` is the control plane (`ssh devexe ls`), any other username
  names a VM and the session is brokered into that VM's sshd (`ssh
  box@devexe`). Key-only auth against `~/.local/share/devexe/authorized_keys`.
- **VM manager** over Apple's Virtualization.framework (Code-Hex/vz).
  An OCI image is pulled with go-containerregistry, flattened, and streamed
  through Microsoft's pure-Go `tar2ext4` into a read-only ext4 base disk —
  the rootfs never touches the host filesystem, so no root is needed and
  ownership/setuid/device nodes survive. Each VM adds a sparse writable
  ext4 data disk (`mke2fs`), joined by overlayfs at boot. The kernel is the
  Kata Containers static arm64 build (the same one Apple's `container`
  direct-boots), fetched once and cached.
- **HTTP front door** (127.0.0.1:8080). `http://<vm>.exe.localhost:8080`
  proxies to the VM — the smallest `EXPOSE`d port, or `share port`. VMs are
  private by default; `ssh devexe share <vm>` prints a signed link,
  `share set-public` opens it up.

Inside every VM, a small static Go agent (`exeguest`) rides the initramfs
as pid 1: it assembles the overlay root, DHCPs on the NAT network, installs
your keys, serves ssh (its own embedded sshd — so even distroless images
are ssh-able), supervises the image's ENTRYPOINT/CMD, and talks to the
daemon over vsock. When macOS's Local Network privacy blocks TCP to the
guest, everything transparently falls back to vsock.

Sessions land as a proper login: the default user `dev` (uid 1000,
passwordless sudo — baked into exeuntu) when the image has it, root
otherwise, running the user's shell from `/etc/passwd` as a login shell in
`$HOME`, with `/etc/motd` shown on interactive connects. scp/sftp run as
the same user, so ownership comes out right. `default_user` in config.toml
changes the preference; `sudo -i` is always one step from root.

VM state machine: `creating → stopped → starting → running → stopping`,
plus `error`. Stopped VMs keep their disk and release CPU/RAM back to the
pool (`ls -l` shows usage). ssh to a stopped VM boots it on demand (~1 s).
If the daemon dies, records reconcile to `stopped` on restart.

## Requirements

- Apple Silicon Mac, macOS 15+
- Go 1.25+, Homebrew (`brew install e2fsprogs`)
- ~600 MB one-time kernel download on first `exed serve`

## Setup

```
make build          # builds exed + guest agent, codesigns (required for vz)
bin/exed install    # ssh config (Host devexe) + authorized_keys from ~/.ssh
bin/exed serve      # run the daemon in the foreground
bin/exed doctor     # if something is off
```

## Commands

```
ssh devexe new [name] [--image ref] [--cpu N] [--memory MB] [--disk GB] [--no-start]
                                   # default image: exeuntu
ssh devexe ls [-l] [--json]
ssh devexe start|stop|restart <vm>...
ssh devexe rm <vm>...
ssh devexe cp <src> <dst>          # instant clone (APFS copy-on-write)
ssh devexe rename <old> <new>
ssh devexe share <vm>              # signed URL for a private vm
ssh devexe share set-public|set-private <vm>
ssh devexe share port <vm> <port>
ssh devexe ssh-key ls|add
ssh devexe whoami | doc | browser <vm>
```

`scp`/`sftp` and `ssh -L` work through the gateway:

```
scp file.txt box@devexe:/root/
ssh -L 8080:localhost:80 web@devexe
```

## Development

```
make test           # unit tests (no VMs, no signing needed)
make build          # rebuild + codesign
bin/spike -image alpine:latest   # standalone boot smoke test
```

State lives in `~/.local/share/devexe/` (VM records, disks, keys), caches
in `~/Library/Caches/devexe/` (kernel, base disks by image digest). Serial
console of each VM: `~/.local/share/devexe/vms/<name>/serial.log`.

## Caveats

- VMs die with the daemon (Virtualization.framework VMs live in-process);
  records reconcile to `stopped` on restart.
- `*.exe.localhost` resolves in browsers; for curl use
  `curl --resolve box.exe.localhost:8080:127.0.0.1 …`.
- Images run under the agent as pid 1 — systemd in the image is not
  executed (ubuntu works fine; `systemctl` does not).
- No TLS on the front door, no `ssh -R`, no ssh-agent forwarding yet.
