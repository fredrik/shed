# Instructions for coding agents

shed is a local clone of exe.dev: real Linux microVMs on Apple's
hypervisor, managed over ssh. README.md explains what it is and how it
works — read it first. This file is the operational knowledge that isn't
obvious from the code.

## Build and run

- `make build` — builds `bin/shedd` (+ the guest agent) and codesigns it.
  Plain `go build`/`go run` produces a binary that cannot boot VMs
  (missing the com.apple.security.virtualization entitlement). It also
  injects the version (`git describe`) via ldflags; `bin/shed version`
  shows client and daemon builds and warns if a stale daemon is running.
- `make test` — unit tests; builds the linux guest agent first because
  internal/initramfs embeds it (`go test ./...` alone fails on a clean tree).
- `bin/shedd serve` — foreground daemon (ssh gateway :2222, http :8080).

## Branches and worktrees

- Never edit in the main checkout at `~/code/fredrik/shed`, and never
  switch its branch; it stays on `main`. Several agent sessions run in
  parallel, each in its own worktree, and Fredrik uses the main checkout
  himself.
- Do all work on a feature branch in a worktree under
  `.claude/worktrees/<branch>` (gitignored). Create it before touching
  any file; build and test inside it.
- Base the branch on `origin/main`, not the local `main` ref, which may
  be behind origin/main or carry other commits.
- Commit in logical chunks as you go.
- A change a user of shed would notice (commands, flags, image
  contents, defaults, kernel) gets a line under `Unreleased` in
  CHANGELOG.md, with the PR number. Refactors, tests and docs don't.

## Testing in sandboxed sessions

- Control commands need no ssh: `bin/shed ls|new|rm|...` talks to the
  daemon over `~/.local/share/shed/control.sock` (no keys involved).
  Note: macOS caps unix socket paths at ~104 bytes, so a deep
  SHED_STATE_DIR (e.g. the scratchpad) fails to bind — use a short one.
- ssh is only needed for shells/scp/-L into a VM. Fredrik's ssh-agent is
  not reachable from sandboxed shells and his key has a passphrase; use a
  dedicated test key: generate one with ssh-keygen, `bin/shed ssh-key
  add -` it, then `ssh -o IdentityAgent=none -o IdentitiesOnly=yes -i
  <key> <vm>@shed`.
- zsh does not word-split unquoted variables — use `${=VAR}` for option
  variables holding ssh/curl flags.
- Remove every VM you create once you are done testing (`bin/shed rm
  <vm>`; verify with `bin/shed ls`). Never remove VMs you didn't create.

## Platform facts (verified — do not re-derive)

- Guest kernel: shed's own build (kernel/ has the recipe; Linux 6.18.15
  with Kata 3.28.0's fragments and patches), published as a release
  asset on the kernel-<version> tag and cached at
  `~/Library/Caches/shed/kernel/<version>/Image`; sha pinned in
  internal/kernel. virtio blk/net/console/vsock, ext4, overlayfs,
  virtiofs, erofs and 9p are built in; modules are not. `SHED_KERNEL`
  points shedd at any other Image; scripts/kernel-daemon.sh runs a
  throwaway daemon on it beside the live one.
- macOS 15 Local Network privacy (TCC) blocks host→guest TCP on bridge100
  in this environment ("no route to host"). The vsock fallback in
  vzbackend.DialGuest handles it — do not debug it as a network failure.
- vsock ports: guest→host control on 2048; guest port-forward listener on
  1024; a baking VM serves its rootfs tar on guest loopback 1025 (see
  internal/vsockproto).
- The default image "sheduntu" is baked locally on first use by
  internal/vm/sheduntu.go (recipe and cache-key logic live there); cached
  as `~/Library/Caches/shed/base/sheduntu-<hash>.img` with a .json sidecar.
  Recipe or upstream-digest changes rebake on next use (about a minute).
  VMs are pinned to the base they were created on (vm.json image.digest,
  see internal/vm/basedisk.go); a rebake never changes an existing VM's
  lower layer, and prune keeps every bake a VM still references.
