# Instructions for coding agents

The task: build a local clone of exe.dev, the ssh-based microvm service.
Status: core is built and working — see README.md for architecture and usage.

## Build and run

- `make build` — builds `bin/exed` (+ guest agent) and codesigns it. Plain
  `go build`/`go run` produces a binary that cannot boot VMs (missing the
  com.apple.security.virtualization entitlement).
- `make test` — unit tests; builds the linux guest agent first because
  internal/initramfs embeds it (`go test ./...` alone fails on a clean tree).
- `bin/exed serve` — foreground daemon (ssh gateway :2222, http :8080).
- `cmd/spike` — standalone image→boot→ssh smoke test; kept for debugging.

## Testing over ssh in this sandbox

The user's ssh-agent is not reachable from sandboxed shells and their key
has a passphrase. Use a dedicated test key: generate one with ssh-keygen,
append the pubkey to `~/.local/share/devexe/authorized_keys`, then
`ssh -o IdentityAgent=none -o IdentitiesOnly=yes -i <key> devexe ls`.
zsh does not word-split unquoted variables — use `${=VAR}` for option vars.

## Known platform facts (verified, do not re-derive)

- Kernel: Kata static arm64 `vmlinux-6.18.15-186` (kata 3.28.0), cached at
  `~/Library/Caches/devexe/kernel/3.28.0/Image`; sha pinned in internal/kernel.
- macOS 15 Local Network TCC blocks host→guest TCP on bridge100 in this
  environment ("no route to host"); the vsock fallback in
  vzbackend.DialGuest handles it — do not debug it as a network failure.
- guest→host vsock control is on port 2048; guest port-forward listener on
  vsock 1024; a baking VM serves its rootfs tar on guest loopback 1025
  (see internal/vsockproto).
- The default image "exeuntu" is baked locally on first use by
  internal/vm/exeuntu.go (recipe + cache-key logic there); cached under
  ~/Library/Caches/devexe/base/exeuntu-<hash>.img with a .json sidecar.
