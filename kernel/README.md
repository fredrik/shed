# shed's guest kernel

Every shed VM boots the same kernel: a monolithic arm64 Linux, built
from the recipe in this directory and published as a release asset on
this repository. `internal/kernel` downloads that asset once, pins it
by SHA-256, and re-hashes the cached copy on every daemon start.

## What is in it

Upstream stable Linux at the tag in `versions`, with Kata Containers'
patches for that series applied and Kata's arm64 config fragments
merged the way Kata's own `build-kernel.sh` does it, then `shed.conf`
on top. Kata's kernel is a known-good base for direct-booting on
Virtualization.framework, and is the kernel Apple's `container` uses,
which is why shed started out downloading it. Building it ourselves
means the recipe is ours to change.

The result is what a VM with no module loader needs: virtio blk, net,
console and vsock, ext4, overlayfs, virtiofs, erofs and 9p all built
in, and an uncompressed `Image`, which the VZ Linux boot loader requires
on arm64. Distro kernels fail on both counts.

`uname -r` in a guest reports `6.18.15-shed`.

## Building it

Kernel builds happen inside a shed VM; cross-compiling on macOS is the
version of this that wastes an afternoon. With a daemon running:

```sh
make kernel
```

creates a `kernel-build` VM, copies this directory into it, runs
`build.sh` there, fetches the output into `bin/kernel/` and removes the
VM again. Expect about five minutes, most of it downloading the
toolchain and the source. `build.sh` prints the release string, size and
SHA-256 at the end.

```
bin/kernel/Image          the kernel
bin/kernel/Image.sha256   the value to pin
bin/kernel/config         the .config it was built from
```

The VM is removed whether or not the build succeeded. `KERNEL_CPUS`
sizes it (default 8); `SHED_SSH`, `SHED_SCP` and `SHED_HOST` override
how make reaches the VM if `ssh <vm>@shed` is not how you do it.

## Trying it before publishing

`SHED_KERNEL=<path>` makes shedd boot that Image instead of the pinned
one. The scripts under `scripts/` run a second daemon beside the live
one so the experiment never touches your real VMs:

```sh
scripts/kernel-daemon.sh bin/kernel/Image   # terminal 1: ssh :2223, http :8081
scripts/kernel-shed.sh new ktest            # terminal 2
ssh -p 2223 ktest@127.0.0.1 uname -a
scripts/kernel-shed.sh rm ktest
```

A wrong config typically gives a VM that hangs with no console, so keep
the VM's serial log in view on a first boot. Running `kernel-daemon.sh`
with no argument boots the pinned kernel: the control case.

## Publishing a release

1. Bump `versions` and/or `shed.conf`, `make kernel`, boot-test as above.
2. Pick the tag: `kernel-<upstream>-shed`, so `kernel-6.18.15-shed`.
   A recipe change without an upstream bump still needs a new tag, so
   add a distinguishing suffix rather than moving an existing tag.
3. Publish the Image as the release's only asset:

   ```sh
   gh release create kernel-6.18.15-shed bin/kernel/Image \
     --title "Guest kernel 6.18.15-shed" --notes-file - <<'NOTES'
   Linux 6.18.15, Kata 3.28.0 fragments and patches, CONFIG_LOCALVERSION=-shed.
   sha256 <paste bin/kernel/Image.sha256>
   NOTES
   ```

4. Set `Version` and `imageSHA256` in `internal/kernel/kernel.go` to the
   tag suffix and the printed hash, run `make test`, commit.

## Reproducibility

Same source, same patches, same config procedure as Kata's release, but
Ubuntu's current gcc rather than the gcc 11 Kata built with, and the
kernel embeds a build timestamp. So a rebuild is config-equivalent to
the published Image, not byte-identical. `bin/kernel/config` is the
thing to diff when a rebuild behaves differently.
