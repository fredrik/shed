# Plan vs implementation: mismatch notes

Evaluation of `plans/local-devexe-plan.md` against the code as of 2026-08-18
(commit eca54f3). Written by Claude after a full-repo audit.

## Verdict

The plan was executed faithfully through M4 and most of M5, with one
systematic rename (devexe → shed: `shedd`, `shedguest`, ssh user `shed`,
`*.shed.localhost`, `~/.local/share/shed`). The repo layout, backend
interface seam, two-disk overlay boot, Kata kernel pinning, tar2ext4
pipeline, vsock-fallback `DialGuest`, pool accounting, and startup
reconcile all match the plan closely — several to the letter (pool
defaults are exactly cores−2 / half RAM / 100 GB; the state machine,
`last_stop_reason: "daemon restart"`, and the friendly pool-exhausted
errors are all there). The drift is in the two directions below.

## Missing from the plan (implemented, but the plan never covered it)

- **The exeuntu bake apparatus** — the plan's biggest blind spot. The plan
  listed "optional exeuntu default image" as an M5 stretch; in reality it
  is the *default image* (`internal/config/config.go:52`) and the
  centerpiece of the codebase: a throwaway VM boots ubuntu:24.04, runs a
  provisioning script (`internal/vm/exeuntu.go:32-94`), then a second
  guest mode (`cmd/shedguest/bake.go`) serves the merged rootfs as a tar
  on guest port 1025, streamed host-side straight into `tar2ext4`. The
  cache-key scheme (recipe + upstream digest → auto-rebake), old-bake GC,
  and the `BakeScript`/`BakeTarPort` extensions to `vsockproto` are all
  unplanned.
- **Login-user handling.** The plan's guest contract had no notion of a
  user; the implementation has `default_user` config,
  `vsockproto.Config.User`, `/etc/passwd` parsing without NSS
  (`cmd/shedguest/users.go`), credential-dropping for ssh/sftp sessions,
  login-shell argv, motd, and a re-exec'd `sftp-server` mode so non-root
  file ownership comes out right.
- **Dual-transport guest sshd** — the embedded sshd listens on both TCP
  :22 and vsock :22 (`cmd/shedguest/sshd.go:71-91`) as the TCC-proof
  fallback; the plan only put vsock fallback in `DialGuest`.
- **Progress streaming** (`CreateOpts.Progress`) so `ssh shed new`
  narrates the multi-minute first bake, plus serial-log paths threaded
  into start-failure error messages.
- **`busy` sentinel concurrency** instead of the plan's per-VM locks —
  simpler, and surfaced in user errors (`vm "x" is busy (starting)`).
- **Entrypoint supervision with backoff** (1s→30s exponential, reset
  after a long run, bare-shell CMDs skipped) — the plan said
  "supervision" but none of this shape.
- **devexe→shed migration** in `shedd install`
  (`cmd/shedd/install.go:98-134`).
- Smaller deltas: uncompressed cpio instead of cpio.gz (deliberate,
  `internal/initramfs/initramfs.go:18-19`); httpgate's target ports come
  from `ImageInfo.ExposedPorts` rather than the planned
  `vsockproto.Config` ports field; `GuestIP()` is `(net.IP, bool)`
  without a context; `StartRequest` dropped `InitramfsPath` (and its
  `KernelPath` is dead code — the manager sets it at
  `internal/vm/manager.go:290` but vzbackend uses its own).

## Planned but not implemented

### Commands and flags

- `ssh` control command is missing entirely
  (`internal/control/commands.go:29-44` — the only absent top-level
  command).
- `new` lacks `--command` and `--name` (name became positional with a
  random adjective-noun fallback — arguably better, but a deviation).
- `--json` was planned "everywhere from day one" but exists only on
  `new`/`ls`/`rm` — not on start/stop/restart, cp, rename, whoami,
  share, ssh-key.

### Broker

- No signal forwarding and no exit-signal relay (a non-status exit
  collapses to code 1).
- `ssh -R` and agent forwarding — deferred to M5 in the plan, still
  absent.

### Guest

- **No systemd handoff** (plan §guest agent and M3): zero systemd
  references anywhere; the agent is always pid 1. Documented as a README
  caveat.
- No image-sshd arbitration — the embedded sshd is unconditional.
- **No zombie reaping** — the plan named it as a pid-1 duty; there is no
  SIGCHLD/wait4 loop, so orphaned grandchildren accumulate.

### HTTP front door

- No login page — unauthenticated requests get a 403 explainer, not the
  planned login redirect.
- `share add <email>` exists but is a stated no-op stub; the auth gate
  never consults the email list.
- No wake-on-HTTP (M5) and no local-CA TLS (M5 stretch): a stopped VM
  gets the branded 502 instead of auto-starting.

### Daemon / ops

- **No launchd support** — `install --launchd` doesn't exist; foreground
  `serve` only.
- **No disk grow** — `resize2fs` appears nowhere; `DiskGB` is immutable
  after create.
- **No OCI layout cache**: every create *and start* re-resolves the
  image remotely, so starting an existing VM offline fails; and since
  `Start` re-resolves the tag but keeps the stale entrypoint/cmd config,
  a moved tag boots new rootfs with old config.
- No `deleting` state from the plan's state machine.

### Testing — the weakest area

- No `rogpeppe/go-internal` testscript golden CLI tests at all (the
  plan's "UX specs"); control tests are a hand-rolled fake session with
  substring asserts.
- `make integration` is dead: the `vzintegration` build tag matches zero
  files, `SHED_VZ_TESTS` is read by nothing, and `cmd/codesign` is an
  **empty untracked directory** — the signing wrapper was never written.
  The planned vz integration suite (boot, exec, reconcile-after-kill,
  HTTP roundtrip) doesn't exist.
- Only 4 of ~13 internal packages have tests; sshgate, httpgate, image,
  diskfs, store are untested.

## Bugs surfaced in passing

Not plan gaps, but found during the audit:

- `ssh -L` ignores the destination host and always dials the port inside
  the VM (`internal/sshgate/broker.go:192`).
- sftp always exits 0 regardless of the guest subsystem's status
  (`internal/sshgate/broker.go:166`).
- The bake VM's 2 CPU / 2 GB / 8 GB are invisible to pool accounting,
  and concurrent first-bakes aren't serialized
  (`internal/vm/exeuntu.go:141-156`).
- `SaveVM` errors are silently ignored in the state-transition hot paths
  (`internal/vm/manager.go:315,328,355`).
- The sshgate package comment still says the reserved user is `"exe"`.

## Highest-leverage open items

The integration test path (dead Makefile target + missing
`cmd/codesign`), offline start (OCI layout cache), zombie reaping, and
launchd.
