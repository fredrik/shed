# Design decisions

Decisions that shaped shed, in roughly the order a re-implementer would
meet them. Each entry gives the context, what was chosen, and what that
choice costs. Sources: code comments, the README, the original plan in
`plans/local-devexe-plan.md`, and commit messages. A final section lists
known gaps that are consequences of these decisions rather than bugs.

## D1. Real VMs on Virtualization.framework, not containers

**Context.** The purpose is a place to run things you do not want loose
on your Mac: agents with root, untrusted install scripts, services on
privileged ports.

**Decision.** Every VM is a Linux guest with its own kernel on Apple's
hypervisor, driven through Code-Hex/vz. No Docker, no containerd.

**Consequences.** Strong isolation and a familiar "it is a machine"
model. Boot is about a second rather than milliseconds. VMs live
inside the daemon process (D2). Requires the virtualization entitlement
and therefore codesigning (D22).

## D2. One daemon, VMs in-process, records reconcile on restart

**Context.** Virtualization.framework VMs are objects in the creating
process. There is no daemon-independent VM handle to reattach to.

**Decision.** `shedd` is a single foreground process that owns every
VM. On startup, any record left in a non-terminal state is demoted to
`stopped` with reason `daemon restart`. ssh to a stopped VM boots it on
demand.

**Consequences.** VMs die with the daemon; that is documented as a
caveat rather than hidden. Crash recovery is trivial and truthful. A
launchd mode is a natural follow-up (see the README's foreground
instructions) but is not built.

## D3. ssh is the interface; routing by username

**Context.** exe.dev's entire API is `ssh exe.dev <command>` plus
`ssh <vm>@exe.dev`. That is the experience being cloned.

**Decision.** One gateway on loopback. Username `shed` is the control
plane; any other username is a VM name. No HTTP API, no GUI, no YAML.

**Consequences.** Names must be valid ssh usernames, hostnames and URL
labels at once, hence the narrow regex. Every client on every machine
already has the tooling. scp, sftp and `-L` come along for free once
the broker relays them.

## D4. A local client that speaks ssh over a unix socket

**Context.** Sandboxed sessions and scripts on the same Mac should not
need an ssh agent or a passphrase to run `ls` or `new`.

**Decision.** The gateway serves a second listener on a 0600 unix
socket with client auth disabled. `bin/shed` is a dumb pipe: it joins
argv, runs it as an ssh exec line over the socket, and exits with the
remote code. It contains no command tree.

**Consequences.** One handler, one cobra tree, zero drift between `ssh
shed x` and `shed x`. The socket path is bounded by macOS's ~104-byte
limit on unix socket paths. Interactive shells still go over TCP ssh.

## D5. Broker as an ssh client into the guest, not a TCP splice

**Context.** sshpiper-style routing could either splice the client's
ssh bytes to the guest's sshd or terminate ssh at the gateway and open
a second connection.

**Decision.** Terminate at the gateway. The daemon authenticates the
user, then logs into the guest as root with its own broker key (present
in every guest's authorized keys) and mirrors the session.

**Consequences.** The gateway can create the VM on first connect (D20),
boot on demand, print progress before a guest exists, and implement
`-L` and sftp via `DialGuest`. The user's keys never enter the guest as
credentials for the broker path (they are still installed for direct
use). Costs: a second ssh hop, and anything not explicitly relayed
(terminal modes, agent forwarding, `-R`) is missing.

## D6. OCI image → ext4 via tar2ext4, never extracted on the host

**Context.** A rootfs from a registry must become a block device. On
macOS, extracting to APFS as a normal user loses ownership, device
nodes and some setuid bits, and needs root to do properly.

**Decision.** Pull with go-containerregistry, flatten with
`mutate.Extract`, stream the tar into Microsoft's pure-Go `tar2ext4`
writer. The rootfs never exists as files on the host.

**Consequences.** No root, full fidelity, seconds per image. The result
is a read-only filesystem by construction, which pushes toward D7.
Maximum base filesystem size is fixed at 16 GiB.

## D7. Shared read-only base disk + per-VM sparse data disk + overlayfs

**Context.** N VMs from one image should not cost N copies, and
creating a VM should be instant.

**Decision.** Base disks are cached by image digest and attached
read-only to every VM using that image. Each VM gets a sparse ext4
data disk sized to `disk_gb`. The guest joins them with overlayfs.

**Consequences.** Create is one `mke2fs` on a sparse file (~0.3 s).
Clone is one `clonefile` (D16). A 10 GB disk costs megabytes until
used. The image cannot be modified in place; all changes live on the
data disk. Base disks can be pruned freely and rebuilt.

## D8. mke2fs from Homebrew is the only external dependency

**Context.** Writable ext4 needs a filesystem creator. tar2ext4 only
writes read-only images.

**Decision.** Shell out to Homebrew's `mke2fs` (keg-only path probed
explicitly) with lazy inode table and journal init.

**Consequences.** `brew install e2fsprogs` is a requirement; `doctor`
checks it. Everything else is pure Go. Offline disk growth
(`resize2fs`) would use the same package but is not implemented.

## D9. Kata Containers' static kernel, pinned by hash

**Context.** VZ's Linux boot loader on arm64 needs an uncompressed
`Image`. Distro kernels are compressed and have virtio as modules, so
they cannot mount a virtio root from an initramfs without a module
loader.

**Decision.** Download the Kata 3.28.0 arm64 release tarball once,
extract one member (`vmlinux-6.18.15-186`, the same kernel Apple's
`container` uses), verify its SHA-256, cache it. Monolithic: virtio
blk/net/console/vsock, ext4, overlayfs built in; no modules, no erofs.

**Consequences.** ~600 MB one-time download for a 16 MB file. Kernel
changes are a code change (bump constants). Guests cannot load kernel
modules.

## D10. A Go agent as pid 1; no systemd

**Context.** The guest needs an init that mounts the overlay, configures
the network, serves ssh and starts the workload, on any image
including ones with no init at all.

**Decision.** `shedguest` is `/init` in the initramfs and stays pid 1.
It does the boot in a few hundred milliseconds of straight-line Go and
then supervises the image's `ENTRYPOINT`/`CMD`. Images that ship systemd
do not get it executed.

**Consequences.** Boot is deterministic and image-independent. `systemctl`
does not work inside VMs. The agent must be copied into the real root
before `switch_root` so it can re-exec itself (for sftp as a user).

## D11. An embedded sshd in the guest, ephemeral host key

**Context.** Most OCI images have no sshd. Requiring one would kill the
"any image works" promise.

**Decision.** The agent runs a gliderlabs/ssh server on `:22` (and vsock
22), authenticating against keys delivered at boot, generating a new
ed25519 host key every boot. The gateway does not verify it; trust
comes from the transport.

**Consequences.** Distroless and alpine images are sshable. Sessions
get a proper login (D19). Users who ssh directly to a guest IP would
see host-key changes every boot; the gateway path hides this.

## D12. Per-VM config over vsock, not the kernel command line

**Context.** Hostname, keys, entrypoint and login user differ per VM
and per boot. Putting them on the cmdline or in the initramfs would
make those artifacts per-VM.

**Decision.** The kernel cmdline is the constant `console=hvc0
rdinit=/init`. The guest dials the host over vsock, says hello,
receives a JSON config, and later reports ready or error. The same
connection carries shutdown.

**Consequences.** Base disks and the initramfs are identical for every
VM and therefore cacheable. The handshake doubles as the readiness
signal, so `Start` returns only when the VM is sshable. Loss of the
connection powers the guest off.

## D13. DialGuest with TCP first and vsock fallback

**Context.** macOS 15's Local Network privacy can block host→guest TCP
on the NAT bridge with no dialog, depending on how the process was
launched.

**Decision.** All host→guest connections go through
`RunningVM.DialGuest(port)`. vzbackend tries TCP to the guest IP with a
750 ms timeout, then falls back to a vsock connection to the agent's
forwarder (port 1024), which splices to guest loopback.

**Consequences.** The gateway, front door and bake harvester are
transport-agnostic. Under TCC every connection pays 750 ms; otherwise
nothing. `ssh -L` destinations are always resolved inside the guest,
so the destination host is ignored. A different backend only has to
implement `DialGuest`.

## D14. Deterministic MAC from the VM name

**Decision.** `06:` + five bytes of `SHA-256("shed-mac:" + name)`.

**Consequences.** The same VM gets the same DHCP lease across restarts;
ARP and bootpd logs are readable. Renaming a VM changes its MAC and
therefore its address, which is why rename requires the VM to be
stopped.

## D15. One JSON file per VM, atomic writes, a flock; no database

**Decision.** `vms/<name>/vm.json` written via temp + rename. One
`flock` on `shedd.lock` guarantees a single daemon per state
directory. Pool usage is derived from the records, never persisted.

**Consequences.** State is inspectable with `cat`, recoverable by hand,
and impossible to leave half-written. Listing is O(n) file reads at
startup, which is fine for tens of VMs. Counters cannot drift.

## D16. Clone = stop, clonefile, start

**Decision.** `cp` quiesces a running source, `clonefile(2)`s the data
disk (APFS copy-on-write), shares the base disk, and restarts the
source. Clones start private and without autostart.

**Consequences.** Cloning is a couple of seconds and costs no disk
until blocks diverge. The source is briefly unavailable. Emails and
the public flag are intentionally not inherited.

## D17. Private-by-default HTTP with an HMAC token cookie

**Decision.** A per-install 32-byte secret; token = truncated
HMAC-SHA256 over `shed-share:<name>`; delivered as a query parameter,
pinned as a cookie, stripped by redirect. `set-public` bypasses it.
Stopped VMs are not booted by HTTP requests.

**Consequences.** No per-VM secret to store; links are stable.
Revoking one link means rotating the shared secret. A browser reload
cannot boot machines; only ssh can.

## D18. sheduntu is baked locally with shed's own machinery

**Context.** exe.dev's default image is a tuned Ubuntu. shed wants the
same, without depending on a registry-hosted custom image or on Docker
to build one.

**Decision.** A throwaway VM boots upstream `ubuntu:24.04` with a bake
script in its config, the agent runs the script and serves a tar of
the merged root, and the host turns that into a cached base disk. The
cache key is a hash of the recipe and a manual version string, not the
upstream digest. Old bakes are pruned.

**Consequences.** First use costs about a minute; every later create
resolves offline. Upstream Ubuntu updates require bumping
`sheduntuVersion`. The bake exercises exactly the kernel and agent the
image will run on. The bake VM bypasses the pool.

## D19. Non-root default login user; the agent stays root

**Context.** Landing in a root shell in `/` with a bare environment is
hostile. sheduntu bakes a `dev` user (uid 1000, zsh, passwordless
sudo).

**Decision.** The host sends a preferred user in the boot config. The
agent runs sessions as that user when `/etc/passwd` has it, else root.
Only session children drop privileges; sftp for non-root users
re-execs the agent under the user's credential so ownership is right.
Sessions get a login shell in `$HOME`, a sane `PATH`, and the motd.

**Consequences.** Plain OCI images keep working unchanged. `sudo -i` is
always one step from root. The image, not the agent, defines users.

## D20. Create the VM on first ssh

**Decision.** `ssh box@shed` to a nonexistent `box` creates it with
default options, streaming progress to stderr with CRLF, then
continues into the shell. Invalid names are rejected before anything
is created.

**Consequences.** "Creating a computer is as cheap as creating a file"
becomes literal. Typos create VMs; `rm` is cheap too. A lost race
between two concurrent first connections falls through to the existing
VM.

## D21. Workload supervision skips bare shells

**Decision.** The agent runs `ENTRYPOINT + CMD` as a supervised service
with backoff, but not when the argv is a single bare shell, which is
the CMD of every base image.

**Consequences.** `nginx:latest` serves on boot; `ubuntu:24.04` does
not spawn a doomed non-interactive `bash` every second. Images whose
service happens to be invoked as a bare shell would need an explicit
entrypoint.

## D22. Signing via make; plain go build is unsupported for shedd

**Decision.** `make build` builds the agent, embeds it, builds `shedd`,
and ad-hoc codesigns it with `vz.entitlements`. `doctor` checks the
entitlement is present.

**Consequences.** `go run ./cmd/shedd` starts but cannot boot VMs. Tests
do not need signing because no test boots a VM.

## D23. Uncompressed initramfs, rebuilt on every start

**Decision.** Hand-rolled newc cpio, no compression, containing the
agent and a `/dev/console` node, written atomically to the state
directory at each `serve`.

**Consequences.** No dependency on the kernel's decompressor set. The
guest agent is always the one embedded in the running daemon. A few
megabytes of extra memory per VM at boot, freed after switch_root is
not reclaimed (the old initramfs is not unmounted).

## Known gaps

These follow from the decisions above and are not currently addressed.
They are the first candidates for follow-up work.

- **No TLS on the front door** (D17). Loopback only, single user.
- **No `ssh -R`, no agent forwarding, no terminal-mode relay** (D5).
  The broker only relays what it explicitly mirrors.
- **Client environment variables are not applied guest-side.** The
  gateway forwards `env` requests and the guest server accepts them,
  but the session handler builds the child environment from scratch
  and ignores them (D19).
- **`ssh -L` ignores the destination host** (D13). Every destination is
  the guest itself.
- **No orphan reaper in pid 1** (D10). The agent waits only on children
  it started; processes re-parented to pid 1 after their parent exits
  become zombies.
- **systemd is not executed** (D10). `systemctl` fails inside VMs.
- **VMs die with the daemon** (D2). Reconciled, not survived.
- **No disk growth after create** (D8).
- **Bake VM is not pool-accounted** (D18). A bake on a saturated pool
  still runs.
- **The vsock port 22 sshd listener is unused by the host** (D11,
  D13); the forwarder path is what the fallback actually uses.
- **Kernel modules cannot be loaded** (D9).
- **Foreground daemon only**; no launchd integration (D2).
