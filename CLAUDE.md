# Instructions for coding agents

shed is a local clone of exe.dev: real Linux microVMs on Apple's
hypervisor, managed over ssh. README.md explains what it is and how it
works — read it first. This file is the operational knowledge that isn't
obvious from the code.

## Build and run

- `make build` — builds `bin/shedd` (+ the guest agent) and codesigns it.
  Plain `go build`/`go run` produces a binary that cannot boot VMs
  (missing the com.apple.security.virtualization entitlement).
- `make test` — unit tests; builds the linux guest agent first because
  internal/initramfs embeds it (`go test ./...` alone fails on a clean tree).
- `bin/shedd serve` — foreground daemon (ssh gateway :2222, http :8080).

## Testing over ssh in sandboxed sessions

- Fredrik's ssh-agent is not reachable from sandboxed shells and his key
  has a passphrase. Use a dedicated test key: generate one with
  ssh-keygen, append the pubkey to `~/.local/share/shed/authorized_keys`,
  then `ssh -o IdentityAgent=none -o IdentitiesOnly=yes -i <key> shed ls`.
- zsh does not word-split unquoted variables — use `${=VAR}` for option
  variables holding ssh/curl flags.
- Remove every VM you create once you are done testing (`ssh shed rm
  <vm>`; verify with `ssh shed ls`). Never remove VMs you didn't create.

## Platform facts (verified — do not re-derive)

- Guest kernel: Kata static arm64 `vmlinux-6.18.15-186` (kata 3.28.0),
  cached at `~/Library/Caches/shed/kernel/3.28.0/Image`; sha pinned in
  internal/kernel. virtio blk/net/console/vsock, ext4, overlayfs, and
  virtiofs are built in; erofs and modules are not.
- macOS 15 Local Network privacy (TCC) blocks host→guest TCP on bridge100
  in this environment ("no route to host"). The vsock fallback in
  vzbackend.DialGuest handles it — do not debug it as a network failure.
- vsock ports: guest→host control on 2048; guest port-forward listener on
  1024; a baking VM serves its rootfs tar on guest loopback 1025 (see
  internal/vsockproto).
- The default image "exeuntu" is baked locally on first use by
  internal/vm/exeuntu.go (recipe and cache-key logic live there); cached
  as `~/Library/Caches/shed/base/exeuntu-<hash>.img` with a .json sidecar.
  Recipe or upstream-digest changes rebake on next use (about a minute).
