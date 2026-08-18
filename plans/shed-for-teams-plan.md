# Plan: shed-for-teams — Linux port and private team hosting

## Context

shed (this repo) is a working local clone of exe.dev on macOS: microVMs over
Virtualization.framework, ssh control plane, HTTP front door. See
`local-devexe-plan.md` for the original design; the core is built and running.

This plan covers the next direction: porting the host side to Linux/KVM and
deploying shed as a **private dev-VM service for a team** — one daemon on a
cloud host (e.g. Azure), each teammate gets their own microVMs over ssh. Not a
public SaaS: every user is a known colleague, which eliminates the hard
service problems (abuse prevention, billing, paranoid tenancy walls).

## Decision: one repo, two axes

Considered forking a separate Linux/cloud project; rejected. The product space
is a 2×2 and a fork would cut along the wrong seam:

|  | vz backend | KVM backend |
|---|---|---|
| **Local, single-user** | shed today (Mac) | Linux laptop (future, falls out for free) |
| **Hosted, team** | — | shed-for-teams on Azure |

- **Axis 1 — hypervisor backend**: vz vs KVM. Already abstracted behind
  `internal/backend` (vzbackend, stubbackend prove the seam). A Linux backend
  is a third implementation, selected by GOOS/config.
- **Axis 2 — deployment mode**: local single-user (implicit today in sshgate
  authorized_keys, store paths, `serve`) vs hosted multi-user. Becomes a
  config/auth concern, not a fork.

~90% of the code is platform-neutral (sshgate, httpgate, control, vm manager,
guest agent, image/diskfs pipeline, vsockproto, keys) and the guest side is
already Linux. One repo means protocol and features land once; the classic
fork failure mode is guest-agent/protocol drift.

## Backend: KVM via a Linux VMM

Add `internal/backend/kvmbackend` implementing the existing `Backend` /
`RunningVM` interface using **Firecracker** (first choice — closest analog to
exe.dev's own stack; Cloud Hypervisor as fallback if its API fits better).

- Kernel: same Kata static kernel, x86_64 build (Azure nested virt is
  x86_64-only; their ARM sizes lack it). `internal/kernel` gains per-arch
  artifact pinning. Guest agent cross-compiles to GOARCH=amd64.
- vsock: Linux `vhost-vsock` keeps the same CID/port model; Firecracker
  proxies vsock over unix sockets. `vsockproto` and guest ports
  (2048 control, 1024 forward, 1025 bake) survive unchanged. The macOS TCC
  workaround becomes irrelevant on Linux.
- Networking: tap devices + Linux bridge + nftables NAT replace
  bridge100/NAT. `DialGuest` can use plain TCP to the guest IP.
- Paths: XDG dirs (`~/.local/share/shed`, `~/.cache/shed`) / `/var/lib/shed`
  for a system service; no codesigning or entitlements on Linux.
- Expect some interface churn: the Backend contract is currently vz-shaped
  (one real implementation defines it). First task is making vz and KVM both
  honest implementations rather than special-casing either.

## Hosted mode: multi-user

What "team" actually requires — deliberately modest:

1. **Auth**: map ssh public keys → usernames. A managed keys file
   (per-user sections, or `keys/<user>/authorized_keys`) is enough to start;
   SSO can come later if ever.
2. **Namespacing**: VM names scoped per user in `internal/vm` manager and
   `internal/store`; ssh username routing resolves within the caller's
   namespace. Likely the largest single code change, still small.
3. **Quotas**: per-user CPU/RAM/disk pool limits on top of the existing
   global pool accounting.
4. **Idle policy**: stop VMs after N idle minutes (no ssh session, no HTTP
   traffic), auto-start on connect — the trick that lets one host serve a
   team. Wake-on-connect already half-exists via auto-start-on-ssh.
5. **HTTP front door**: real hostname + wildcard DNS
   (`*.shed.internal.example`), TLS at the edge, share-gate now spans users.

## Deployment target

One nested-virt-capable Azure VM (e.g. D8s_v5, Ubuntu), `shedd` under systemd,
ports 2222/443 reachable over the team VPN/private network. No Kubernetes —
single node is the finish line for this plan; DaemonSet/multi-node is a
possible later chapter, not scoped here.

## Milestones

| # | Scope | Demo |
|---|-------|------|
| **T0 — Linux spike** | Firecracker boots the Kata x86_64 kernel + existing initramfs/agent on a Linux box; vsock handshake; measure boot→ssh. Go/no-go on Firecracker vs Cloud Hypervisor. | Script boots exeuntu to a root shell on Linux. |
| **T1 — kvmbackend** | Backend interface hardened; kvmbackend passes the same integration suite as vzbackend; tap networking; Linux paths; `make build` grows a linux target (no codesign). | `shedd serve` on a Linux laptop/VM; `ssh shed new` boots a real microVM. |
| **T2 — multi-user** | Key→user mapping, per-user VM namespacing, per-user quotas. | Two users on one daemon each see only their own `ls`. |
| **T3 — team deploy** | systemd unit, Azure host provisioning notes, wildcard DNS + TLS on httpgate, idle-stop/wake policy. | Teammate on the VPN runs `ssh shed new` and shares a web port. |

## Key risks

1. **Backend interface churn** touching the working mac path → gate with the
   existing stubbackend test suite; vz integration tests stay green per change.
2. **x86_64 guest path untested** (agent, initramfs, exeuntu bake are
   arm64-proven) → T0 spike covers it end to end before any porting.
3. **Firecracker API mismatch** (e.g. snapshot/clone semantics vs `cp`'s
   clonefile trick) → Cloud Hypervisor fallback; worst case `cp` copies the
   data disk on Linux.
4. **Multi-user assumptions buried in manager/store** → audit for
   name-is-globally-unique assumptions before T2.
