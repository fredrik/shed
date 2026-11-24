# Changelog

User-visible changes to shed, newest first. Format follows
[Keep a Changelog](https://keepachangelog.com); versions are the `v*`
git tags. Guest kernel builds have their own `kernel-<version>` tags and
release notes and only appear here when shed starts using a new one.

## Unreleased

- README brought up to date with the current image, defaults, kernel
  and docs (#23).

## v0.1.0 - 2026-09-22

The first tagged release. shed is a local clone of exe.dev: Linux
microVMs on Apple's Virtualization.framework, managed over ssh.

### Added

- `shedd`, one daemon running the ssh gateway (127.0.0.1:2222), the VM
  manager and the HTTP front door (127.0.0.1:8080).
- ssh routing by username: `ssh shed <cmd>` is the control plane, `ssh
  <vm>@shed` opens a shell in that VM, creating and booting it on demand.
- Control commands: `new`, `ls`, `start`, `stop`, `restart`, `rm`, `cp`
  (copy-on-write clone), `rename`, `share`, `ssh-key`, `whoami`, `doc`,
  `browser`, `version`.
- `bin/shed`, a local client that runs the same commands over a unix
  socket with no ssh keys involved. `ssh-key add -` reads a key from
  stdin.
- OCI images as VM roots: pulled, flattened and converted to an ext4
  base disk in pure Go, with a per-VM writable overlay. Any image works
  via `--image`.
- `shedguest`, a static Go init that assembles the overlay root, DHCPs,
  runs an embedded sshd so even distroless images are reachable, and
  falls back to vsock when macOS Local Network privacy blocks TCP.
- sheduntu, the default image, baked locally on first use from Ubuntu
  26.04: git, curl, vim, tmux, htop, ripgrep, jq, fzf, bat, fd, zoxide,
  tree, the GitHub CLI, mise with node 24, uv with python 3.14, and
  Claude Code for the `dev` user (#3, #9, #13, #19).
- sheduntu login: zsh with a starship prompt, autosuggestions, syntax
  highlighting, ctrl-r/ctrl-t fuzzy search, and Ghostty's terminfo baked
  in (#3, #8, #11).
- Sessions log in as the image's `dev` user (uid 1000, passwordless sudo)
  when present, root otherwise; scp/sftp run as the same user.
- HTTP front door at `http://<vm>.shed.localhost:8080`, private by
  default with signed share links, `share set-public` and `share port`.
- shed's own guest kernel, Linux 6.18.15 with the Kata Containers
  configuration, published as a release asset and fetched on first run
  (#18).
- `shedd install` (ssh config and authorized_keys) and `shedd doctor`.
- Version numbering: `make build` bakes `git describe` into both
  binaries, `shed version` shows client and daemon and warns when they
  differ (#22).
- Architecture and design docs under `docs/design/` (#7).

### Changed

- Renamed from exe/devexe to shed: daemon `shedd`, ssh host `shed`,
  image sheduntu (was exeuntu).
- Default VM memory raised from 2 GB to 4 GB; defaults are 2 vCPUs,
  4 GB, 10 GB disk (#15).
- VM create time cut to about 0.3 s (#2).
- VMs are pinned to the base disk they were created on; a sheduntu
  rebake no longer changes existing VMs, and prune keeps every bake a VM
  still references (#10).
- `shedd` with no subcommand runs `serve`.
