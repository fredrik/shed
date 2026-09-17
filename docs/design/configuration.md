# Configuration, installation and build

What the daemon reads at startup, how the host is prepared, and what
the build must do so the binary is allowed to boot VMs. Packages:
`internal/config`, `internal/keys`, `internal/store`, `cmd/shedd`.

## config.toml

`config.Load()` starts from defaults computed for the machine and then
overlays `<state>/config.toml` if it exists. Unknown keys are ignored.

| Key | Default | Meaning |
|-----|---------|---------|
| `ssh_addr` | `127.0.0.1:2222` | ssh gateway listener |
| `http_addr` | `127.0.0.1:8080` | HTTP front door listener |
| `default_image` | `sheduntu` | image for `new` without `--image` and for create-on-connect |
| `default_user` | `dev` | preferred ssh login user inside VMs, when the image has it |
| `default_cpus` | `2` | vCPUs for `new` without `--cpu` |
| `default_memory_mb` | `4096` | memory for `new` without `--memory` |
| `default_disk_gb` | `10` | data disk size for `new` without `--disk` |
| `[pool] cpus` | `max(NumCPU - 2, 2)` | total vCPUs across running VMs |
| `[pool] memory_mb` | half of `hw.memsize` (8192 if unreadable) | total memory across running VMs |
| `[pool] disk_gb` | `100` | total data disk size across all VMs |

Example:

```toml
default_user = "root"
default_memory_mb = 2048

[pool]
cpus = 8
memory_mb = 16384
disk_gb = 200
```

Two values are derived and not configurable in TOML: the state
directory and the cache directory.

## Environment

| Variable | Effect |
|----------|--------|
| `SHED_STATE_DIR` | Overrides `~/.local/share/shed`. Used by tests and by sandboxed sessions. macOS caps unix socket paths at about 104 bytes, so a deep state directory makes `control.sock` fail to bind. |

The cache directory is always `~/Library/Caches/shed`.

## Daemon startup sequence

`shedd` with no arguments is `shedd serve`. In order:

1. Load config.
2. `store.Open`: create `vms/` (0755) and `keys/` (0700), take an
   exclusive non-blocking `flock` on `shedd.lock`. A second daemon on
   the same state directory fails here with a clear message.
3. Ensure the host key and the broker key exist (`keys/host_ed25519`,
   `keys/broker_ed25519`; ed25519, OpenSSH PEM, 0600, generated on
   first run).
4. Seed `authorized_keys` from `~/.ssh/*.pub` (deduplicated, appended);
   warn if it ends up empty.
5. Ensure the kernel (download and verify on first run, verify
   thereafter).
6. Write the initramfs to `<state>/initramfs.cpio`.
7. Construct the vz backend, the OCI preparer, the manager, and the
   guest-key callback (user keys plus the broker public key, re-read on
   every boot so `ssh-key add` reaches the next VM start).
8. `Manager.Recover`.
9. Ensure the share secret.
10. Start the ssh TCP listener, the ssh unix socket listener, and the
    HTTP listener in goroutines; start autostart VMs; log `shed ready`.
11. Block until a listener fails or SIGINT/SIGTERM arrives. On signal:
    `StopAll` with a 30 s budget, close both gateways, exit 0.

## `shedd install`

Idempotent host preparation, safe to re-run:

- Creates `<state>/keys`.
- Seeds `authorized_keys` from `~/.ssh/*.pub` and reports the count,
  with a hint to run `ssh-keygen` if zero.
- Writes `~/.ssh/shed_config`:

  ```
  Host shed
    HostName 127.0.0.1
    Port 2222
    User shed
    UserKnownHostsFile ~/.local/share/shed/known_hosts
    StrictHostKeyChecking accept-new
  ```

  Host and port come from `ssh_addr`. A dedicated known_hosts file
  keeps the gateway's key out of the user's main file and lets a state
  directory reset not produce host-key warnings elsewhere.
- Prepends `Include shed_config` to `~/.ssh/config` if not already
  present (OpenSSH requires `Include` before any `Host` block to apply
  globally).
- Prints next steps.

`install` does not write a launchd agent; the daemon runs in the
foreground.

## `shedd doctor`

Prints a pass/fail line per check and exits non-zero if any fail:

| Check | Hint on failure |
|-------|-----------------|
| Binary is signed with `com.apple.security.virtualization` (via `codesign -d --entitlements`) | build with `make build`, not `go build` |
| `mke2fs` present at a Homebrew e2fsprogs path | `brew install e2fsprogs` |
| `<cache>/kernel` exists | downloaded on first `shedd serve` |
| `ssh_addr` and `http_addr` are free to bind | another process (or shedd) is listening |

## Build and signing

Virtualization.framework refuses to create VMs from a process without
the `com.apple.security.virtualization` entitlement. `vz.entitlements`
declares exactly that one key. The Makefile:

```
make agent   GOOS=linux GOARCH=arm64 CGO_ENABLED=0 -trimpath -ldflags="-s -w"
             -> internal/initramfs/shedguest_linux_arm64   (gitignored, go:embed'ed)
make build   agent; go build ./cmd/shedd; codesign --entitlements vz.entitlements -f -s - bin/shedd
             go build ./cmd/shed                            (no signing needed)
make test    agent; go test ./...                           (embed requires the agent to exist)
make clean   rm -rf bin internal/initramfs/shedguest_linux_arm64
```

Ad-hoc signing (`-s -`) is sufficient for local use. A plain `go build`
or `go run` of `shedd` produces a binary that starts, serves ssh, and
fails at the first VM boot; `doctor` catches this. `bin/shed` needs no
entitlement because it never touches the hypervisor.

Requirements: Apple Silicon, macOS 15+, Go 1.25+, Homebrew e2fsprogs.

## Testing

`make test` runs unit tests only; no VM is booted and no signing is
needed. Coverage by package:

- `internal/vm`: create/start/stop/remove flow, pool exhaustion,
  guest-initiated power-off settling the record, start failure →
  `error`, recover demoting `running`, rename and duplicate names,
  sheduntu reference matching and pruning. Uses `stubbackend` and a
  fake preparer.
- `internal/vm/vmspec`: name validation, MAC stability and
  locally-administered bit.
- `internal/control`: `new`/`ls`/`rm` flow, invalid command exit code,
  empty command shows help, share flow, `ssh-key add -` from stdin,
  quoted argument parsing. Uses a fake `control.Session`.
- `internal/sshgate`: a real ssh client over the unix socket to a
  gateway with no auth, exercising the control path end to end.
- `internal/initramfs`: archive shape and that `init` is the largest
  entry.

Integration testing against real VMs is manual: `make build`,
`bin/shedd`, and the commands in `CLAUDE.md` for sandboxed sessions
(dedicated test key, short `SHED_STATE_DIR`).

## Spec notes

- Config file: `<state>/config.toml`, TOML, keys as tabled above;
  missing file means defaults.
- State directory: `$SHED_STATE_DIR` or `~/.local/share/shed`. Cache
  directory: `~/Library/Caches/shed`, fixed.
- Daemon lock: `flock(LOCK_EX|LOCK_NB)` on `<state>/shedd.lock`.
- Keys: ed25519, OpenSSH private key format, 0600, at
  `keys/host_ed25519` and `keys/broker_ed25519`; broker public key is
  appended to every guest's authorized keys with comment `shed-broker`.
- `authorized_keys`: OpenSSH format, seeded from `~/.ssh/*.pub`,
  deduplicated by key blob, comment defaults to the source filename.
- ssh client config written by install: `Host shed`, `User shed`,
  `UserKnownHostsFile <state>/known_hosts`, `StrictHostKeyChecking
  accept-new`, included from `~/.ssh/config` via `Include shed_config`.
- Signals: SIGINT and SIGTERM trigger graceful stop of all VMs (30 s)
  and exit.
- Entitlement: `com.apple.security.virtualization`, ad-hoc signature.
