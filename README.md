# shed

A clone of [exe.dev](https://exe.dev), the ssh-based microVM service, that runs
entirely on your own Mac. Real Linux microVMs booted from OCI images in about
a second, managed over ssh, with persistent disks and an HTTP front door.

```
$ ssh box@shed
shed: creating vm box...
dev@box:~$
```

## Introduction to shed

shed keeps a pile of small Linux machines behind your Mac. The idea comes
from exe.dev: creating a computer should be about as cheap as creating a
file. shed does the same thing but entirely on your own hardware, so there
is no account and nothing metered.

Each machine is a real VM on Apple's hypervisor, with its own kernel,
init, and disk — not a container. That's the point: it's where you put
work you don't want running loose on your Mac. A coding agent with root, a
`curl | sh` you don't quite trust, a service that wants port 80. Whatever
happens inside stays inside; the VM can't see your Mac's disk or
processes.

VMs are cheap enough that you don't have to be tidy. A stopped VM costs
nothing but its disk, and ssh boots it again in about a second, so it's
fine to have ten half-finished experiments sitting in `ls`. Everything is
addressed by name: `ssh box@shed` opens a shell (creating the VM first if
it doesn't exist), `ssh shed cp box box2`
clones the whole machine in a couple of seconds, and
`http://box.shed.localhost:8080` reaches whatever box is serving. There is
no GUI and no YAML; the interface is ssh.

The default image is exeuntu (after exe.dev's own): Ubuntu 24.04 with the
usual tools installed (git, curl, vim, tmux, htop, ripgrep, jq), a `dev`
user with passwordless sudo, plus mise and uv in /usr/local/bin, node 24
via mise, and python 3.14 (uv-managed) as dev's default next to the apt
`python3`. It's baked locally the first time you use it: a throwaway VM
boots upstream `ubuntu:24.04`, runs the recipe, and its rootfs becomes the
cached base image. Takes about a minute, rebakes when the upstream digest
or the recipe changes, and old bakes are pruned. Any other OCI image works
via `--image`.

## How it works

One daemon (`shedd`) runs three things:

- **SSH gateway** (127.0.0.1:2222). Routing is by username, sshpiper-style:
  user `shed` is the control plane (`ssh shed ls`), any other username
  names a VM and the session is brokered into that VM's sshd (`ssh
  box@shed`). Key-only auth against `~/.local/share/shed/authorized_keys`.
  The same gateway also listens on a local unix socket with no client
  auth (the 0600 socket is the auth) — that's what `bin/shed` talks to.
- **VM manager** over Apple's Virtualization.framework (Code-Hex/vz).
  An OCI image is pulled with go-containerregistry, flattened, and streamed
  through Microsoft's pure-Go `tar2ext4` into a read-only ext4 base disk —
  the rootfs never touches the host filesystem, so no root is needed and
  ownership/setuid/device nodes survive. Each VM adds a sparse writable
  ext4 data disk (`mke2fs`), joined by overlayfs at boot. The kernel is the
  Kata Containers static arm64 build (the same one Apple's `container`
  direct-boots), fetched once and cached.
- **HTTP front door** (127.0.0.1:8080). `http://<vm>.shed.localhost:8080`
  proxies to the VM — the smallest `EXPOSE`d port, or `share port`. VMs are
  private by default; `ssh shed share <vm>` prints a signed link,
  `share set-public` opens it up.

Inside every VM, a small static Go agent (`shedguest`) rides the initramfs
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
- ~600 MB one-time kernel download on first `shedd serve`

## Setup

```
make build          # builds shedd + guest agent, codesigns (required for vz)
bin/shedd install    # ssh config (Host shed) + authorized_keys from ~/.ssh
bin/shedd            # run the daemon in the foreground (same as shedd serve)
bin/shedd doctor     # if something is off
```

## Commands

```
ssh shed new [name] [--image ref] [--cpu N] [--memory MB] [--disk GB] [--no-start]
                                   # default image: exeuntu
ssh shed ls [-l] [--json]
ssh shed start|stop|restart <vm>...
ssh shed rm <vm>...
ssh shed cp <src> <dst>          # instant clone (APFS copy-on-write)
ssh shed rename <old> <new>
ssh shed share <vm>              # signed URL for a private vm
ssh shed share set-public|set-private <vm>
ssh shed share port <vm> <port>
ssh shed ssh-key ls|add          # add - reads the key from stdin
ssh shed whoami | doc | browser <vm>
```

Locally, `bin/shed` runs the same commands without ssh: it talks to the
daemon over a unix socket (`~/.local/share/shed/control.sock`, 0600 — the
file mode is the auth), so no keys or agent are involved. `shed ls`,
`shed new mybox`, `cat key.pub | shed ssh-key add -`. The client sends
its argv line to the daemon verbatim; the command surface is one cobra
tree either way. Interactive shells still go over ssh.

`scp`/`sftp` and `ssh -L` work through the gateway:

```
scp file.txt box@shed:/root/
ssh -L 8080:localhost:80 web@shed
```

## Development

```
make test           # unit tests (no VMs, no signing needed)
make build          # rebuild + codesign
```

State lives in `~/.local/share/shed/` (VM records, disks, keys), caches
in `~/Library/Caches/shed/` (kernel, base disks by image digest). Serial
console of each VM: `~/.local/share/shed/vms/<name>/serial.log`.

## Caveats

- VMs die with the daemon (Virtualization.framework VMs live in-process);
  records reconcile to `stopped` on restart.
- `*.shed.localhost` resolves in browsers; for curl use
  `curl --resolve box.shed.localhost:8080:127.0.0.1 …`.
- Images run under the agent as pid 1 — systemd in the image is not
  executed (ubuntu works fine; `systemctl` does not).
- No TLS on the front door, no `ssh -R`, no ssh-agent forwarding yet.
